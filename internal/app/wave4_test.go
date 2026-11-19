package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/2118505078xyh1/argent-go/internal/cli"
	"github.com/2118505078xyh1/argent-go/internal/llm"
	"github.com/2118505078xyh1/argent-go/internal/security"
	"github.com/2118505078xyh1/argent-go/internal/session"
)

// scriptedChatter returns one scripted answer per request, without a network.
type scriptedChatter struct {
	mu      sync.Mutex
	replies []string
	calls   int
	systems []string
}

func (f *scriptedChatter) Chat(_ context.Context, _ string, messages []llm.Message, _ []llm.ToolSpec, onText func(string)) (llm.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	for _, message := range messages {
		if message.Role == "system" {
			f.systems = append(f.systems, message.Content)
		}
	}
	n := f.calls
	text := "ok"
	if n <= len(f.replies) {
		text = f.replies[n-1]
	}
	if onText != nil {
		onText(text)
	}
	return llm.Message{Role: "assistant", Content: text}, nil
}

func (f *scriptedChatter) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func testStore(t *testing.T, res Resolved) *session.Store {
	t.Helper()
	store, err := session.Open(filepath.Join(res.Storage, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestAddDirAllowsWritesInsideAndRejectsOutside(t *testing.T) {
	root := t.TempDir()
	extra := t.TempDir()
	outside := t.TempDir()
	env, _ := testEnv(t)
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: root, AddDirs: []string{extra}})
	if err != nil {
		t.Fatal(err)
	}
	store := testStore(t, res)
	sess, err := store.NewSession("add-dir")
	if err != nil {
		t.Fatal(err)
	}
	set := newToolset(context.Background(), cli.IO{Env: env}, res, sess, nil)
	defer set.Close()
	ctx := context.Background()

	target := filepath.Join(extra, "nested", "note.txt")
	args := fmt.Sprintf(`{"path":%q,"content":"hello"}`, target)
	if _, err := set.Run(ctx, "write", args); err != nil {
		t.Fatalf("write inside --add-dir: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "hello" {
		t.Fatalf("file = %q, err = %v", data, err)
	}
	if out, err := set.Run(ctx, "read", fmt.Sprintf(`{"path":%q}`, target)); err != nil || !strings.Contains(out, "hello") {
		t.Fatalf("read inside --add-dir: out=%q err=%v", out, err)
	}

	outsideArgs := fmt.Sprintf(`{"path":%q,"content":"nope"}`, filepath.Join(outside, "x.txt"))
	if _, err := set.Run(ctx, "write", outsideArgs); err == nil {
		t.Fatal("write outside every root must fail")
	}
	relative, err := filepath.Rel(root, filepath.Join(extra, "escape.txt"))
	if err != nil {
		t.Fatal(err)
	}
	escapeArgs := fmt.Sprintf(`{"path":%q,"content":"nope"}`, relative)
	if _, err := set.Run(ctx, "write", escapeArgs); err == nil {
		t.Fatal("a relative .. escape must fail even when it lands in an add dir")
	}
}

func TestAddDirGuardAllowsInsideAndDeniesOutside(t *testing.T) {
	root := t.TempDir()
	extra := t.TempDir()
	outside := t.TempDir()
	env, _ := testEnv(t)
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: root, AddDirs: []string{extra}})
	if err != nil {
		t.Fatal(err)
	}
	store := testStore(t, res)
	sess, err := store.NewSession("add-dir-guard")
	if err != nil {
		t.Fatal(err)
	}
	policy := security.NewPolicy(root, security.PolicyConfig{WriteRoots: res.WriteRoots})
	chain := security.NewChain(policy, autoApprover{}, nil, sess.ID())
	set := newToolset(context.Background(), cli.IO{Env: env}, res, sess, chain)
	defer set.Close()
	ctx := context.Background()

	if _, err := set.Run(ctx, "write", fmt.Sprintf(`{"path":%q,"content":"ok"}`, filepath.Join(extra, "a.txt"))); err != nil {
		t.Fatalf("guarded write inside --add-dir: %v", err)
	}
	_, err = set.Run(ctx, "write", fmt.Sprintf(`{"path":%q,"content":"no"}`, filepath.Join(outside, "b.txt")))
	if !errors.Is(err, security.ErrDenied) {
		t.Fatalf("write outside roots: err = %v, want ErrDenied", err)
	}
}

func TestResumeAppendsToTheAddressedSession(t *testing.T) {
	env, _ := testEnv(t)
	root := t.TempDir()
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: root})
	if err != nil {
		t.Fatal(err)
	}
	chatter := &scriptedChatter{replies: []string{"one", "two"}}
	res.Chatter = chatter
	store := testStore(t, res)
	existing, err := store.NewSession("existing")
	if err != nil {
		t.Fatal(err)
	}
	res.Options.Session = existing.ID()
	runner := &runner{stdio: cli.IO{Env: env}, res: res, store: store}

	for i, want := range []string{"one", "two"} {
		got, err := runner.Prompt(context.Background(), fmt.Sprintf("prompt %d", i+1), nil)
		if err != nil || got != want {
			t.Fatalf("prompt %d: got %q, err %v, want %q", i+1, got, err, want)
		}
	}
	if got := runner.SessionID(); got != existing.ID() {
		t.Fatalf("active session = %q, want %q", got, existing.ID())
	}
	if got := ActiveSessionID(runner); got != existing.ID() {
		t.Fatalf("ActiveSessionID = %q, want %q", got, existing.ID())
	}
	records, err := existing.Records()
	if err != nil {
		t.Fatal(err)
	}
	users := 0
	for _, record := range records {
		if record.Kind == session.KindUser {
			users++
		}
	}
	if users != 2 {
		t.Fatalf("user records = %d, want 2 appended to the same session", users)
	}
}

func TestResumeWithoutIDOpensNewestSession(t *testing.T) {
	env, _ := testEnv(t)
	store, err := session.Open(filepath.Join(DefaultStorage(env), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.NewSession("first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.NewSession("second")
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Append("message", map[string]any{"role": "user", "content": "newer"}); err != nil {
		t.Fatal(err)
	}
	got, err := openOrCreateSession(store, cli.Options{Resume: true}, "ignored")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID() != second.ID() {
		t.Fatalf("resumed %q, want the newest %q (first was %q)", got.ID(), second.ID(), first.ID())
	}
	byPath := filepath.Join(store.Dir(), second.ID()+".jsonl")
	got, err = openOrCreateSession(store, cli.Options{Session: byPath}, "ignored")
	if err != nil || got.ID() != second.ID() {
		t.Fatalf("resume by path: got %q, err %v", idOf(got), err)
	}
}

func idOf(sess *session.Session) string {
	if sess == nil {
		return ""
	}
	return sess.ID()
}

func TestOutputSchemaValidationSubset(t *testing.T) {
	schema := `{"type":"object","required":["ok","items"],"properties":{` +
		`"ok":{"type":"boolean"},` +
		`"items":{"type":"array","items":{"type":"string"}},` +
		`"mode":{"enum":["fast","slow"]}}}`
	cases := []struct {
		name  string
		text  string
		valid bool
	}{
		{"valid object", `{"ok":true,"items":["a"],"mode":"fast"}`, true},
		{"fenced object", "```json\n" + `{"ok":false,"items":[]}` + "\n```", true},
		{"plain text", "here is the answer", false},
		{"wrong type", `{"ok":"yes","items":[]}`, false},
		{"missing required", `{"ok":true}`, false},
		{"bad item type", `{"ok":true,"items":[1]}`, false},
		{"enum mismatch", `{"ok":true,"items":[],"mode":"turbo"}`, false},
		{"trailing prose", `{"ok":true,"items":[]} thanks`, false},
	}
	for _, tc := range cases {
		err := validateOutputText(schema, tc.text)
		if tc.valid && err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
		if !tc.valid && err == nil {
			t.Errorf("%s: invalid output accepted", tc.name)
		}
	}
}

func TestExecuteRetriesOnceForOutputSchema(t *testing.T) {
	env, _ := testEnv(t)
	schema := `{"type":"object","required":["ok"],"properties":{"ok":{"type":"boolean"}}}`
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: t.TempDir(), OutputSchema: schema})
	if err != nil {
		t.Fatal(err)
	}
	chatter := &scriptedChatter{replies: []string{"not json", `{"ok":true}`}}
	res.Chatter = chatter
	store := testStore(t, res)
	sess, err := store.NewSession("schema")
	if err != nil {
		t.Fatal(err)
	}
	result, err := Execute(context.Background(), cli.IO{Env: env}, res, sess, "prompt", nil)
	if err != nil {
		t.Fatalf("Execute after successful retry: %v", err)
	}
	if result.Text != `{"ok":true}` {
		t.Fatalf("text = %q, want the corrected JSON", result.Text)
	}
	if chatter.callCount() != 2 {
		t.Fatalf("model calls = %d, want 2", chatter.callCount())
	}
	if len(chatter.systems) < 2 || !strings.Contains(chatter.systems[1], "did not satisfy the required output schema") {
		t.Fatalf("retry system prompt = %q", chatter.systems)
	}
}

func TestExecuteFailsAfterOneFailedSchemaRetry(t *testing.T) {
	env, _ := testEnv(t)
	schema := `{"type":"object","required":["ok"],"properties":{"ok":{"type":"boolean"}}}`
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: t.TempDir(), OutputSchema: schema})
	if err != nil {
		t.Fatal(err)
	}
	chatter := &scriptedChatter{replies: []string{"still wrong", "also wrong"}}
	res.Chatter = chatter
	store := testStore(t, res)
	sess, err := store.NewSession("schema-fail")
	if err != nil {
		t.Fatal(err)
	}
	_, err = Execute(context.Background(), cli.IO{Env: env}, res, sess, "prompt", nil)
	if err == nil || !strings.Contains(err.Error(), "after one retry") {
		t.Fatalf("err = %v, want a clear post-retry error", err)
	}
	if chatter.callCount() != 2 {
		t.Fatalf("model calls = %d, want exactly 2", chatter.callCount())
	}
}

func TestExecuteAcceptsValidSchemaOnFirstTry(t *testing.T) {
	env, _ := testEnv(t)
	schema := `{"type":"object","properties":{"ok":{"type":"boolean"}}}`
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: t.TempDir(), OutputSchema: schema})
	if err != nil {
		t.Fatal(err)
	}
	chatter := &scriptedChatter{replies: []string{`{"ok":true}`}}
	res.Chatter = chatter
	store := testStore(t, res)
	sess, err := store.NewSession("schema-ok")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(context.Background(), cli.IO{Env: env}, res, sess, "prompt", nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if chatter.callCount() != 1 {
		t.Fatalf("model calls = %d, want 1", chatter.callCount())
	}
}

func TestNoAutoCompactSkipsPostRunStep(t *testing.T) {
	env, _ := testEnv(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "argent.json"), `{"compaction":{"threshold":10}}`)
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: root})
	if err != nil {
		t.Fatal(err)
	}
	if res.AutoCompactTokens != 10 {
		t.Fatalf("configured threshold = %d, want 10", res.AutoCompactTokens)
	}
	store := testStore(t, res)
	build := func() *session.Session {
		sess, err := store.NewSession("compact")
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 10; i++ {
			if err := sess.Append("user", map[string]any{"content": strings.Repeat("x", 200)}); err != nil {
				t.Fatal(err)
			}
		}
		return sess
	}
	withCompaction := build()
	maybeAutoCompact(res, withCompaction)
	if !hasCompaction(withCompaction) {
		t.Fatal("default run must compact above the threshold")
	}
	noCompact := res
	noCompact.NoAutoCompact = true
	skipped := build()
	maybeAutoCompact(noCompact, skipped)
	if hasCompaction(skipped) {
		t.Fatal("--no-auto-compact must skip the post-run step")
	}
}

func hasCompaction(sess *session.Session) bool {
	records, err := sess.Records()
	if err != nil {
		return false
	}
	for _, record := range records {
		if record.Kind == session.KindCompaction {
			return true
		}
	}
	return false
}
