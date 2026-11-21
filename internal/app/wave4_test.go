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

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/security"
	"github.com/2118505078xyh1/argentgo/internal/session"
)

// scriptedChatter 为每个请求返回一个脚本化答案，不使用网络。
type scriptedChatter struct {
	mu         sync.Mutex
	replies    []string
	calls      int
	systems    []string
	specCounts []int
}

func (f *scriptedChatter) Chat(_ context.Context, _ string, messages []llm.Message, toolSpecs []llm.ToolSpec, onText func(string)) (llm.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.specCounts = append(f.specCounts, len(toolSpecs))
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
	set := newToolset(context.Background(), cli.IO{Env: env}, res, sess, nil, nil)
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
	set := newToolset(context.Background(), cli.IO{Env: env}, res, sess, chain, nil)
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
	runner := &runner{stdio: cli.IO{Env: env}, res: res, store: store, execute: Execute}

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

// TestExecuteSchemaRetryReplacesFirstAnswerOnClient 锁定重复文本 bug：纠正轮次必须替换而非追加已流式输出到 onText 的文本，
// 且不得向模型公布工具。
// （接上行）
func TestExecuteSchemaRetryReplacesFirstAnswerOnClient(t *testing.T) {
	env, _ := testEnv(t)
	schema := `{"type":"object","required":["ok"],"properties":{"ok":{"type":"boolean"}}}`
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: t.TempDir(), OutputSchema: schema})
	if err != nil {
		t.Fatal(err)
	}
	chatter := &scriptedChatter{replies: []string{"not json", `{"ok":true}`}}
	res.Chatter = chatter
	store := testStore(t, res)
	sess, err := store.NewSession("schema-replace")
	if err != nil {
		t.Fatal(err)
	}
	var streamed strings.Builder
	result, err := Execute(context.Background(), cli.IO{Env: env}, res, sess, "prompt", func(chunk string) {
		streamed.WriteString(chunk)
	})
	if err != nil {
		t.Fatalf("Execute after successful retry: %v", err)
	}
	if got := streamed.String(); got != `{"ok":true}` {
		t.Fatalf("client text = %q, want only the corrected JSON", got)
	}
	if result.Text != `{"ok":true}` {
		t.Fatalf("result text = %q", result.Text)
	}
	if chatter.callCount() != 2 {
		t.Fatalf("model calls = %d, want 2", chatter.callCount())
	}
	if len(chatter.specCounts) != 2 || chatter.specCounts[1] != 0 {
		t.Fatalf("corrective call advertised %v tool specs, want none", chatter.specCounts)
	}
}

// mutatingThenInvalidChatter 请求一次 write，然后用未通过 schema 校验的文本回答。第三次调用将意味着被禁止的重试。
// （接上行）
type mutatingThenInvalidChatter struct {
	mu    sync.Mutex
	calls int
}

func (c *mutatingThenInvalidChatter) Chat(_ context.Context, _ string, _ []llm.Message, _ []llm.ToolSpec, onText func(string)) (llm.Message, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	switch c.calls {
	case 1:
		return llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{
			ID: "c1", Type: "function",
			Function: llm.ToolCallFunction{Name: "write", Arguments: `{"path":"side-effect.txt","content":"x"}`},
		}}}, nil
	case 2:
		if onText != nil {
			onText("not json")
		}
		return llm.Message{Role: "assistant", Content: "not json"}, nil
	default:
		if onText != nil {
			onText(`{"ok":true}`)
		}
		return llm.Message{Role: "assistant", Content: `{"ok":true}`}, nil
	}
}

// TestExecuteSchemaRetrySkippedAfterMutatingTool 锁定副作用 bug：一旦变更型工具已运行，schema 失败就不得重新运行提示，
// 因为那会第二次执行同一个工具。
// （接上行）
func TestExecuteSchemaRetrySkippedAfterMutatingTool(t *testing.T) {
	env, _ := testEnv(t)
	root := t.TempDir()
	schema := `{"type":"object","required":["ok"],"properties":{"ok":{"type":"boolean"}}}`
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: root, OutputSchema: schema})
	if err != nil {
		t.Fatal(err)
	}
	res.Approve = autoApprover{}
	chatter := &mutatingThenInvalidChatter{}
	res.Chatter = chatter
	store := testStore(t, res)
	sess, err := store.NewSession("schema-mutating")
	if err != nil {
		t.Fatal(err)
	}
	_, err = Execute(context.Background(), cli.IO{Env: env}, res, sess, "prompt", nil)
	if err == nil || !strings.Contains(err.Error(), "mutating tool already ran") {
		t.Fatalf("err = %v, want the skip-retry validation error", err)
	}
	if chatter.calls != 2 {
		t.Fatalf("model calls = %d, want 2 (no corrective retry)", chatter.calls)
	}
	data, err := os.ReadFile(filepath.Join(root, "side-effect.txt"))
	if err != nil || string(data) != "x" {
		t.Fatalf("mutating tool ran with data %q, err %v", data, err)
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
	maybeAutoCompact(res, withCompaction, agent.CompactionEvidence{}, nil)
	if !hasCompaction(withCompaction) {
		t.Fatal("default run must compact above the threshold")
	}
	noCompact := res
	noCompact.NoAutoCompact = true
	skipped := build()
	maybeAutoCompact(noCompact, skipped, agent.CompactionEvidence{}, nil)
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
