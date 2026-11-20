package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/config"
	"github.com/2118505078xyh1/argentgo/internal/hooks"
	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// recordingDispatcher stands in for the shell dispatcher so tests observe
// exactly which events a real Execute produced.
type recordingDispatcher struct {
	mu       sync.Mutex
	payloads []hooks.Payload
	result   hooks.Result
	err      error
	panicOn  string
}

func (f *recordingDispatcher) Dispatch(_ context.Context, _ *hooks.Config, payload hooks.Payload) (hooks.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.payloads = append(f.payloads, payload)
	if strings.EqualFold(payload.Event, f.panicOn) {
		panic("hook dispatcher exploded")
	}
	if f.err != nil {
		return hooks.Result{}, f.err
	}
	return f.result, nil
}

func (f *recordingDispatcher) events() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.payloads))
	for _, payload := range f.payloads {
		out = append(out, payload.Event)
	}
	return out
}

func (f *recordingDispatcher) find(event string) (hooks.Payload, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, payload := range f.payloads {
		if payload.Event == event {
			return payload, true
		}
	}
	return hooks.Payload{}, false
}

// eventResultDispatcher returns a distinct Result per event, so a test can give
// PreToolUse a non-blocking message while leaving PostToolUse silent.
type eventResultDispatcher struct {
	mu      sync.Mutex
	results map[string]hooks.Result
	events  []string
}

func (d *eventResultDispatcher) Dispatch(_ context.Context, _ *hooks.Config, payload hooks.Payload) (hooks.Result, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.events = append(d.events, payload.Event)
	return d.results[payload.Event], nil
}

const hookDocument = `{"hooks":{
	"SessionStart":[{"hooks":[{"command":"true"}]}],
	"UserPromptSubmit":[{"hooks":[{"command":"true"}]}],
	"PreToolUse":[{"hooks":[{"command":"true"}]}],
	"PostToolUse":[{"hooks":[{"command":"true"}]}],
	"PostToolUseFailure":[{"hooks":[{"command":"true"}]}],
	"Stop":[{"hooks":[{"command":"true"}]}],
	"SessionEnd":[{"hooks":[{"command":"true"}]}]
}}`

func trustHookFile(t *testing.T, path, storage string) {
	t.Helper()
	trust := hooks.NewTrustStore()
	if _, err := trust.TrustFile(path); err != nil {
		t.Fatal(err)
	}
	if err := trust.SaveFile(filepath.Join(storage, "hooks-trust.json")); err != nil {
		t.Fatal(err)
	}
}

func TestHookBridgeLoadsTrustedProjectDocument(t *testing.T) {
	root := t.TempDir()
	storage := t.TempDir()
	path := filepath.Join(root, ".argent", "hooks.json")
	writeFile(t, path, hookDocument)
	trustHookFile(t, path, storage)

	fake := &recordingDispatcher{}
	res := Resolved{
		Root: root, Storage: storage, Home: t.TempDir(),
		Cfg:            config.Config{Raw: map[string]any{}},
		HookDispatcher: fake,
	}
	bridge := newHookBridge(res, "session-1", nil)
	if bridge == nil {
		t.Fatal("trusted document produced no bridge")
	}
	bridge.start(context.Background(), "hello")
	bridge.stop(context.Background(), "hello", "answer")
	bridge.end()

	want := []string{"SessionStart", "UserPromptSubmit", "Stop", "SessionEnd"}
	got := fake.events()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("events = %v, want %v", got, want)
	}
	for _, payload := range []string{"SessionStart", "UserPromptSubmit", "Stop", "SessionEnd"} {
		p, _ := fake.find(payload)
		if p.SessionID != "session-1" || p.CWD != root {
			t.Errorf("%s payload = %+v", payload, p)
		}
	}
	if p, _ := fake.find("UserPromptSubmit"); p.Prompt != "hello" {
		t.Errorf("prompt = %q, want hello", p.Prompt)
	}
	if p, _ := fake.find("Stop"); p.Output != "answer" {
		t.Errorf("stop output = %q, want answer", p.Output)
	}
}

func TestHookBridgeSkipsUntrustedAndMissingDocuments(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".argent", "hooks.json")
	writeFile(t, path, hookDocument)
	res := Resolved{
		Root: root, Storage: t.TempDir(), Home: t.TempDir(),
		Cfg: config.Config{Raw: map[string]any{}},
	}
	if bridge := newHookBridge(res, "s", nil); bridge != nil {
		t.Fatal("an untrusted document must not be wired")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if bridge := newHookBridge(res, "s", nil); bridge != nil {
		t.Fatal("no documents must leave hooks optional")
	}
}

func TestHookBridgeLoadsInlineDocumentOnlyWhenDigested(t *testing.T) {
	root := t.TempDir()
	storage := t.TempDir()
	inline := map[string]any{"hooks": map[string]any{
		"Stop": []any{map[string]any{"hooks": []any{map[string]any{"command": "true"}}}},
	}}
	data, err := json.Marshal(inline)
	if err != nil {
		t.Fatal(err)
	}
	trust := hooks.NewTrustStore()
	trust.Trust(inlineHookPath, hooks.Digest(data))
	if err := trust.SaveFile(filepath.Join(storage, "hooks-trust.json")); err != nil {
		t.Fatal(err)
	}
	fake := &recordingDispatcher{}
	res := Resolved{
		Root: root, Storage: storage, Home: t.TempDir(),
		Cfg:            config.Config{Raw: map[string]any{"hooks": inline}},
		HookDispatcher: fake,
	}
	if bridge := newHookBridge(res, "s", nil); bridge == nil {
		t.Fatal("digested inline document was not loaded")
	} else {
		bridge.lifecycle(context.Background(), hooks.Stop, hooks.Payload{})
	}
	if _, ok := fake.find("Stop"); !ok {
		t.Fatal("inline document did not dispatch Stop")
	}
}

func TestHookBridgeAcceptsConfigPathAndTildePath(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	storage := t.TempDir()
	path := filepath.Join(home, ".argent", "my-hooks.json")
	writeFile(t, path, hookDocument)

	fake := &recordingDispatcher{}
	res := Resolved{
		Root: root, Storage: storage, Home: home,
		Cfg:            config.Config{Raw: map[string]any{"hooks": "~/.argent/my-hooks.json"}},
		HookDispatcher: fake,
	}
	// The trust key is the resolved path, matching `argent hooks`.
	trustHookFile(t, path, storage)
	if bridge := newHookBridge(res, "s", nil); bridge == nil {
		t.Fatal("config path document was not loaded")
	} else {
		bridge.lifecycle(context.Background(), hooks.SessionStart, hooks.Payload{})
	}
	if _, ok := fake.find("SessionStart"); !ok {
		t.Fatal("config path document did not dispatch")
	}
}

func TestExecuteFiresHooksAndBlocksPreToolUse(t *testing.T) {
	env, _ := testEnv(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".argent", "hooks.json"), hookDocument)
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: root})
	if err != nil {
		t.Fatal(err)
	}
	trustHookFile(t, filepath.Join(root, ".argent", "hooks.json"), res.Storage)
	fake := &recordingDispatcher{result: hooks.Result{Blocked: true, Message: "blocked by hook"}}
	res.HookDispatcher = fake
	res.Approve = autoApprover{}
	res.Chatter = &toolCallChatter{first: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{
		ID: "c1", Type: "function",
		Function: llm.ToolCallFunction{Name: "write", Arguments: `{"path":"hooked.txt","content":"x"}`},
	}}}}
	store := testStore(t, res)
	sess, err := store.NewSession("hooks-block")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(context.Background(), cli.IO{Env: env}, res, sess, "prompt", nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "hooked.txt")); !os.IsNotExist(err) {
		t.Fatalf("exit-2 PreToolUse still ran the tool (stat err=%v)", err)
	}
	got := strings.Join(fake.events(), ",")
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "Stop", "SessionEnd"} {
		if !strings.Contains(got, event) {
			t.Errorf("missing %s in hook events %q", event, got)
		}
	}
	if strings.Contains(got, "PostToolUse") {
		t.Errorf("a blocked tool must not fire PostToolUse: %q", got)
	}
	if !toolOutputContains(t, sess, "blocked by a PreToolUse hook") {
		t.Fatal("the block was not visible to the model")
	}
}

func TestExecutePostToolUseSeesOutputAndHookFailureIsVisible(t *testing.T) {
	env, _ := testEnv(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "note.txt"), "hello-from-file")
	writeFile(t, filepath.Join(root, ".argent", "hooks.json"), hookDocument)
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: root})
	if err != nil {
		t.Fatal(err)
	}
	trustHookFile(t, filepath.Join(root, ".argent", "hooks.json"), res.Storage)
	fake := &recordingDispatcher{panicOn: "PostToolUse"}
	res.HookDispatcher = fake
	res.Approve = autoApprover{}
	res.Chatter = &toolCallChatter{first: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{
		ID: "c1", Type: "function",
		Function: llm.ToolCallFunction{Name: "read", Arguments: `{"path":"note.txt"}`},
	}}}}
	store := testStore(t, res)
	sess, err := store.NewSession("hooks-output")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(context.Background(), cli.IO{Env: env}, res, sess, "prompt", nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !toolOutputContains(t, sess, "dispatch panicked") {
		t.Fatal("a hook panic was not converted into a tool-visible error")
	}
	if !toolOutputContains(t, sess, "hello-from-file") {
		t.Fatal("the tool output was lost")
	}
}

func TestExecutePreToolUseNonBlockingMessageReachesModel(t *testing.T) {
	env, _ := testEnv(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "note.txt"), "hello-from-file")
	writeFile(t, filepath.Join(root, ".argent", "hooks.json"), hookDocument)
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: root})
	if err != nil {
		t.Fatal(err)
	}
	trustHookFile(t, filepath.Join(root, ".argent", "hooks.json"), res.Storage)
	fake := &eventResultDispatcher{results: map[string]hooks.Result{
		"PreToolUse": {Message: "pre-tool advice"},
	}}
	res.HookDispatcher = fake
	res.Approve = autoApprover{}
	res.Chatter = &toolCallChatter{first: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{
		ID: "c1", Type: "function",
		Function: llm.ToolCallFunction{Name: "read", Arguments: `{"path":"note.txt"}`},
	}}}}
	store := testStore(t, res)
	sess, err := store.NewSession("hooks-pre-message")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(context.Background(), cli.IO{Env: env}, res, sess, "prompt", nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !toolOutputContains(t, sess, "pre-tool advice") {
		t.Fatalf("non-blocking PreToolUse message was dropped: %v", fake.events)
	}
	if !toolOutputContains(t, sess, "hello-from-file") {
		t.Fatal("the tool output was lost")
	}
}

func TestExecuteFiresPostToolUseFailureOnToolError(t *testing.T) {
	env, _ := testEnv(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".argent", "hooks.json"), hookDocument)
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: root})
	if err != nil {
		t.Fatal(err)
	}
	trustHookFile(t, filepath.Join(root, ".argent", "hooks.json"), res.Storage)
	fake := &recordingDispatcher{}
	res.HookDispatcher = fake
	res.Approve = autoApprover{}
	res.Chatter = &toolCallChatter{first: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{
		ID: "c1", Type: "function",
		Function: llm.ToolCallFunction{Name: "read", Arguments: `{"path":"missing.txt"}`},
	}}}}
	store := testStore(t, res)
	sess, err := store.NewSession("hooks-failure")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(context.Background(), cli.IO{Env: env}, res, sess, "prompt", nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	payload, ok := fake.find("PostToolUseFailure")
	if !ok {
		t.Fatalf("PostToolUseFailure not fired: %v", fake.events())
	}
	if payload.Tool != "read" || !strings.Contains(payload.Error, "missing.txt") {
		t.Fatalf("failure payload = %+v", payload)
	}
}

// fileChangedHookDocument subscribes only to FileChanged, so a run with this
// document proves the app fires the event after a real file mutation.
const fileChangedHookDocument = `{"hooks":{
	"FileChanged":[{"hooks":[{"command":"true"}]}]
}}`

func TestExecuteFiresFileChangedAfterWrite(t *testing.T) {
	env, _ := testEnv(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".argent", "hooks.json"), fileChangedHookDocument)
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: root})
	if err != nil {
		t.Fatal(err)
	}
	trustHookFile(t, filepath.Join(root, ".argent", "hooks.json"), res.Storage)
	fake := &recordingDispatcher{}
	res.HookDispatcher = fake
	res.Approve = autoApprover{}
	res.Chatter = &toolCallChatter{first: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{
		ID: "c1", Type: "function",
		Function: llm.ToolCallFunction{Name: "write", Arguments: `{"path":"src/changed.txt","content":"x"}`},
	}}}}
	store := testStore(t, res)
	sess, err := store.NewSession("file-changed")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(context.Background(), cli.IO{Env: env}, res, sess, "prompt", nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "src", "changed.txt")); err != nil {
		t.Fatalf("write tool did not create the file: %v", err)
	}
	payload, ok := fake.find("FileChanged")
	if !ok {
		t.Fatalf("FileChanged not fired: %v", fake.events())
	}
	// The path is workspace-relative and slash-normalised, so a hook matcher
	// such as "*.txt" sees the affected file rather than the tool name.
	if payload.Path != "src/changed.txt" {
		t.Fatalf("FileChanged path = %q, want src/changed.txt", payload.Path)
	}
	if payload.SessionID != sess.ID() || payload.CWD != root {
		t.Fatalf("FileChanged run context = %+v", payload)
	}
}

func TestExecuteDoesNotFireFileChangedForReadOnlyTool(t *testing.T) {
	env, _ := testEnv(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "note.txt"), "hello")
	writeFile(t, filepath.Join(root, ".argent", "hooks.json"), fileChangedHookDocument)
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: root})
	if err != nil {
		t.Fatal(err)
	}
	trustHookFile(t, filepath.Join(root, ".argent", "hooks.json"), res.Storage)
	fake := &recordingDispatcher{}
	res.HookDispatcher = fake
	res.Approve = autoApprover{}
	// A read call carries a path argument but mutates nothing; it must not
	// produce a FileChanged event.
	res.Chatter = &toolCallChatter{first: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{
		ID: "c1", Type: "function",
		Function: llm.ToolCallFunction{Name: "read", Arguments: `{"path":"note.txt"}`},
	}}}}
	store := testStore(t, res)
	sess, err := store.NewSession("no-file-changed")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(context.Background(), cli.IO{Env: env}, res, sess, "prompt", nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if _, ok := fake.find("FileChanged"); ok {
		t.Fatalf("read-only tool fired FileChanged: %v", fake.events())
	}
}

func TestHookedToolsFileChangedFiresPerPathAndAttachesAdvice(t *testing.T) {
	fake := &recordingDispatcher{result: hooks.Result{Message: "advice"}}
	bridge := &hookBridge{dispatcher: fake, config: &hooks.Config{}, sessionID: "s", cwd: "/root"}
	h := &hookedTools{
		bridge: bridge,
		mutationPaths: func(_, _ string) []string {
			return []string{"a.txt", "b.txt"}
		},
	}
	out := "original"
	h.fireFileChanged(context.Background(), "write", `{"path":"a.txt"}`, &out)
	paths := map[string]bool{}
	for _, payload := range fake.payloads {
		if payload.Event != "FileChanged" {
			t.Errorf("unexpected event %q", payload.Event)
			continue
		}
		paths[payload.Path] = true
		if payload.SessionID != "s" || payload.CWD != "/root" {
			t.Errorf("run context not filled: %+v", payload)
		}
	}
	if !paths["a.txt"] || !paths["b.txt"] || len(fake.payloads) != 2 {
		t.Fatalf("mutation paths = %v, payloads = %+v", paths, fake.payloads)
	}
	if strings.Count(out, "advice") != 2 || !strings.HasPrefix(out, "original") {
		t.Fatalf("hook advice not attached without replacing output: %q", out)
	}
}

func TestHookedToolsFileChangedNoopWithoutMutationPaths(t *testing.T) {
	fake := &recordingDispatcher{}
	h := &hookedTools{bridge: &hookBridge{dispatcher: fake, config: &hooks.Config{}}}
	out := "unchanged"
	h.fireFileChanged(context.Background(), "write", `{}`, &out)
	if len(fake.payloads) != 0 || out != "unchanged" {
		t.Fatalf("no-registry case emitted %v / %q", fake.payloads, out)
	}
}

func TestExecuteWithoutHookDocumentsDoesNotDispatch(t *testing.T) {
	env, _ := testEnv(t)
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	fake := &recordingDispatcher{}
	res.HookDispatcher = fake
	res.Chatter = &scriptedChatter{replies: []string{"ok"}}
	store := testStore(t, res)
	sess, err := store.NewSession("no-hooks")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(context.Background(), cli.IO{Env: env}, res, sess, "prompt", nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(fake.events()) != 0 {
		t.Fatalf("dispatch called with no hook documents: %v", fake.events())
	}
}
