package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/memory"
)

type collectingSink struct{ messages []string }

func (s *collectingSink) Add(text string) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}
	s.messages = append(s.messages, text)
	return true
}

func writeInstructionFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestInstructionReconcile(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	agents := filepath.Join(root, "AGENTS.md")
	writeInstructionFile(t, agents, "rules v1")
	env := func(key string) string {
		if key == "HOME" {
			return home
		}
		return ""
	}

	r := NewInstructionReconciler()
	// 首次调用只建立基线，不产出消息。
	if got := r.Reconcile(root, env, true); len(got) != 0 {
		t.Fatalf("first reconcile should seed baseline, got %q", got)
	}
	// 无变化零开销。
	if got := r.Reconcile(root, env, true); len(got) != 0 {
		t.Fatalf("unchanged instructions should yield no messages, got %q", got)
	}

	// 修改规则文件：必须产出带路径与新内容的更新通知。
	writeInstructionFile(t, agents, "rules v2")
	got := r.Reconcile(root, env, true)
	if len(got) != 1 {
		t.Fatalf("changed instructions = %q, want 1 message", got)
	}
	if !strings.Contains(got[0], "Updated instructions from:") || !strings.Contains(got[0], agents) {
		t.Fatalf("update notice missing header/path: %q", got[0])
	}
	if !strings.Contains(got[0], "rules v2") || strings.Contains(got[0], "rules v1") {
		t.Fatalf("update notice must carry new content only: %q", got[0])
	}
	if got := r.Reconcile(root, env, true); len(got) != 0 {
		t.Fatalf("baseline should advance after a change, got %q", got)
	}

	// 删除规则文件：产出移除通知。
	if err := os.Remove(agents); err != nil {
		t.Fatal(err)
	}
	got = r.Reconcile(root, env, true)
	if len(got) != 1 || !strings.Contains(got[0], "Instructions removed:") {
		t.Fatalf("removed instructions = %q, want removal notice", got)
	}

	// ReconcileInto 把消息交给 sink，并在 sink 拒绝时空转。
	writeInstructionFile(t, agents, "rules v3")
	sink := &collectingSink{}
	if n := r.ReconcileInto(sink, root, env, true); n != 1 || len(sink.messages) != 1 {
		t.Fatalf("ReconcileInto added %d, sink has %q", n, sink.messages)
	}
	if !strings.Contains(sink.messages[0], "rules v3") {
		t.Fatalf("sink message missing new content: %q", sink.messages[0])
	}
}

func TestInstructionReconcileDisabledByZero(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	agents := filepath.Join(root, "AGENTS.md")
	writeInstructionFile(t, agents, "v1")
	env := func(key string) string {
		switch key {
		case "HOME":
			return home
		case "ARGENT_INSTRUCTION_RECONCILE":
			return "0"
		}
		return ""
	}

	r := NewInstructionReconciler()
	r.Reconcile(root, env, true) // seed
	writeInstructionFile(t, agents, "v2")
	if got := r.Reconcile(root, env, true); len(got) != 0 {
		t.Fatalf("=0 must disable reconcile, got %q", got)
	}
}

// TestProjectMemoryDocsDedupesContainedByDefault 锁定 #11 的默认接线：
// 系统提示路径真正应用了整段包含去重，而不仅是在纯函数里可用。
func TestProjectMemoryDocsDedupesContainedByDefault(t *testing.T) {
	base := t.TempDir()
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(base, "src", "parser")
	parent := "shared rule\n\nsecond rule"
	child := "local heading\n\n" + parent + "\n\nlocal tail"
	writeInstructionFile(t, filepath.Join(base, "AGENTS.md"), parent)
	writeInstructionFile(t, filepath.Join(cwd, "AGENTS.md"), child)
	env := func(key string) string {
		if key == "HOME" {
			return home
		}
		return ""
	}

	docs := projectMemoryDocs(cwd, env, true)
	if len(docs) != 1 {
		t.Fatalf("docs = %#v, want only the more specific child doc", docs)
	}
	if docs[0].Text != child {
		t.Fatalf("remaining doc = %q, want child", docs[0].Text)
	}

	// =0 回退到旧的逐份注入，保留父级副本。
	off := func(key string) string {
		if key == "HOME" {
			return home
		}
		if key == "ARGENT_DEDUPE_CONTAINED_DOCS" {
			return "0"
		}
		return ""
	}
	if docs := projectMemoryDocs(cwd, off, true); len(docs) != 2 {
		t.Fatalf("=0 fallback should keep both docs, got %#v", docs)
	}
}

func TestRenderInstructionChangesRedactsAndBounds(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "repo")
	secretText := "API_KEY=sk-abcdefghijklmnopqrstuvwxyz0123456789\n" + strings.Repeat("汉", 9000)
	changes := []memory.InstructionChange{
		{Path: filepath.Join(root, "AGENTS.md"), Kind: memory.InstructionUpdated, Text: secretText},
	}
	msgs := renderInstructionChanges(changes)
	if len(msgs) != 1 {
		t.Fatalf("msgs = %q", msgs)
	}
	if strings.Contains(msgs[0], "sk-abcdefghijklmnopqrstuvwxyz") {
		t.Fatalf("secret must be redacted: %q", msgs[0])
	}
	if len(msgs[0]) > instructionMsgBudget {
		t.Fatalf("message too long: %d bytes", len(msgs[0]))
	}
}
