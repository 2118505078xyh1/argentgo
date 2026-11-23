package security

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFileBackedScopedApproverAlwaysSurvivesRestart 验证 always 授权跨重启生效，
// 且规则文件只含作用域（工具名），不含自由文本参数。
func TestFileBackedScopedApproverAlwaysSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	first := NewFileBackedScopedApprover(&fakeApprover{answer: true}, dir, nil)
	ok, err := first.Ask(context.Background(), "bash", `{"command":"git status"}`)
	if err != nil || !ok {
		t.Fatalf("first approval: ok=%v err=%v", ok, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, persistentApprovalsFile))
	if err != nil {
		t.Fatalf("read approvals file: %v", err)
	}
	if bytes.Contains(data, []byte("git status")) || bytes.Contains(data, []byte("command")) {
		t.Fatalf("approvals file must never store free-text args: %s", data)
	}
	var line persistentGrant
	if err := json.Unmarshal(bytes.TrimSpace(data), &line); err != nil {
		t.Fatalf("approvals line is not valid JSON: %v (%q)", err, data)
	}
	if line.Kind != "always" || line.Scope != "bash" {
		t.Fatalf("grant = %+v, want always/bash", line)
	}

	// 新进程：inner 现在拒绝，但持久化的 always 仍应放行同一工具。
	second := NewFileBackedScopedApprover(&fakeApprover{answer: false}, dir, nil)
	ok, err = second.Ask(context.Background(), "bash", `{"command":"rm -rf /"}`)
	if err != nil || !ok {
		t.Fatalf("restart always grant: ok=%v err=%v", ok, err)
	}
	// 不同作用域不被波及，仍需询问（inner 拒绝）。
	ok, err = second.Ask(context.Background(), "write", `{}`)
	if err != nil || ok {
		t.Fatalf("unrelated scope should still ask: ok=%v err=%v", ok, err)
	}
}

// TestFileBackedScopedApproverStickyDenyWins 验证粘性 deny 优先于 always，
// 且无论授予/拒绝顺序如何，重启后仍保持拒绝。
func TestFileBackedScopedApproverStickyDenyWins(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*FileBackedScopedApprover)
	}{
		{"deny after always", func(f *FileBackedScopedApprover) { f.GrantAlways("bash"); f.Deny("bash") }},
		{"deny before always", func(f *FileBackedScopedApprover) { f.Deny("bash"); f.GrantAlways("bash") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			f := NewFileBackedScopedApprover(&fakeApprover{answer: true}, dir, nil)
			tc.setup(f)
			if ok, err := f.Ask(context.Background(), "bash", "{}"); err != nil || ok {
				t.Fatalf("denied scope: ok=%v err=%v", ok, err)
			}
			// 重启后 inner 放行，但持久化的 deny 仍优先。
			g := NewFileBackedScopedApprover(&fakeApprover{answer: true}, dir, nil)
			if ok, err := g.Ask(context.Background(), "bash", "{}"); err != nil || ok {
				t.Fatalf("restart sticky deny: ok=%v err=%v", ok, err)
			}
		})
	}
}

// TestFileBackedScopedApproverCorruptionFailsClosed 验证文件损坏时失败即拒绝：
// 即使同一文件里存在合法的 always 规则，损坏行也会让整份文件不可信。
func TestFileBackedScopedApproverCorruptionFailsClosed(t *testing.T) {
	validAlways := `{"v":1,"kind":"always","scope":"bash","ts":1}` + "\n"
	cases := []struct {
		name     string
		contents string
	}{
		{"garbage", "not-json\n"},
		{"unknown kind", `{"v":1,"kind":"maybe","scope":"bash","ts":1}` + "\n"},
		{"truncated", `{"v":1,"kind":"always"` + "\n"},
		{"valid line plus garbage", validAlways + "garbage\n"},
		{"dirty scope", `{"v":1,"kind":"always","scope":"bash\u0000","ts":1}` + "\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, persistentApprovalsFile)
			if err := os.WriteFile(path, []byte(tc.contents), 0o600); err != nil {
				t.Fatalf("seed approvals file: %v", err)
			}
			f := NewFileBackedScopedApprover(&fakeApprover{answer: true}, dir, nil)
			ok, err := f.Ask(context.Background(), "bash", "{}")
			if ok || !errors.Is(err, errPersistentUntrusted) {
				t.Fatalf("corrupt rules must fail closed: ok=%v err=%v", ok, err)
			}
			// 失败态下不得再写入任何规则。
			f.GrantAlways("read")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read approvals file: %v", err)
			}
			if bytes.Contains(data, []byte(`"scope":"read"`)) {
				t.Fatalf("failed approver must not persist new grants: %s", data)
			}
		})
	}
}

// TestFileBackedScopedApproverFeedbackNeverPersisted 验证带反馈的拒绝不会被落盘：
// 自由文本反馈只交回链转成模型可读错误，绝不写进规则文件。
func TestFileBackedScopedApproverFeedbackNeverPersisted(t *testing.T) {
	dir := t.TempDir()
	const feedback = "请改用 git status --short，这是更正意见"
	inner := &fakeApprover{answer: false, err: &FeedbackRefusal{Feedback: feedback}}
	f := NewFileBackedScopedApprover(inner, dir, nil)
	ok, err := f.Ask(context.Background(), "bash", `{"command":"git status"}`)
	if ok || err == nil {
		t.Fatalf("feedback refusal: ok=%v err=%v", ok, err)
	}
	var refusal *FeedbackRefusal
	if !errors.As(err, &refusal) || refusal.Feedback != feedback {
		t.Fatalf("feedback must pass through untouched: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, persistentApprovalsFile))
	if err == nil && (bytes.Contains(data, []byte(feedback)) || bytes.Contains(data, []byte("git status"))) {
		t.Fatalf("free-text feedback/args must never be persisted: %s", data)
	}
}

// TestFileBackedScopedApproverInjectedScope 验证注入的作用域提取器生效，
// 且非法提取结果会安全回落到工具名，不会污染规则文件。
func TestFileBackedScopedApproverInjectedScope(t *testing.T) {
	cases := []struct {
		name      string
		scopeFor  func(string, string) string
		wantScope string
	}{
		{"valid prefix", func(name, args string) string { return "shell:git" }, "shell:git"},
		{"illegal falls back", func(name, args string) string { return "带有控制符\x00" }, "bash"},
		{"nil falls back", nil, "bash"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			f := NewFileBackedScopedApprover(&fakeApprover{answer: true}, dir, tc.scopeFor)
			if ok, err := f.Ask(context.Background(), "bash", `{"command":"git status"}`); err != nil || !ok {
				t.Fatalf("approval: ok=%v err=%v", ok, err)
			}
			data, err := os.ReadFile(filepath.Join(dir, persistentApprovalsFile))
			if err != nil {
				t.Fatalf("read approvals file: %v", err)
			}
			if !strings.Contains(string(data), `"scope":"`+tc.wantScope+`"`) {
				t.Fatalf("scope = %s, want %s", data, tc.wantScope)
			}
			if bytes.Contains(data, []byte("git status")) {
				t.Fatalf("args must not be persisted: %s", data)
			}
		})
	}
}

// TestFileBackedScopedApproverEmptyStorageIsMemoryOnly 验证空存储目录时零文件 I/O，
// 行为退化为纯内存：授予仍即时生效，拒绝不跨进程。
func TestFileBackedScopedApproverEmptyStorageIsMemoryOnly(t *testing.T) {
	f := NewFileBackedScopedApprover(&fakeApprover{answer: true}, "", nil)
	f.GrantAlways("bash")
	ok, err := f.Ask(context.Background(), "bash", "{}")
	if err != nil || !ok {
		t.Fatalf("in-memory always grant: ok=%v err=%v", ok, err)
	}
	if f.path != "" {
		t.Fatalf("empty storage must not derive a file path, got %q", f.path)
	}
}
