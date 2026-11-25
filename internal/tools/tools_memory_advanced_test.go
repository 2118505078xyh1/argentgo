package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"example.com/argentgo/internal/memory"
)

func TestBankReflectIsExtractiveRelevantAndDedupes(t *testing.T) {
	bank := memory.NewBank(t.TempDir())
	for _, text := range []string{
		"alpha beta gamma delta",
		"Alpha Beta Gamma Delta.", // near-duplicate: case + trailing punctuation
		"alpha beta epsilon",
	} {
		if _, err := bank.Retain(text, nil, ""); err != nil {
			t.Fatal(err)
		}
	}
	got := bank.Reflect("alpha beta", 10)
	if !strings.HasPrefix(got, "Reflection (extractive synthesis):") {
		t.Fatalf("missing extractive header: %q", got)
	}
	if !strings.Contains(got, "- ") {
		t.Fatalf("missing '- ' line prefix: %q", got)
	}
	lower := strings.ToLower(got)
	if n := strings.Count(lower, "alpha beta gamma"); n != 1 {
		t.Fatalf("near-duplicate text appeared %d times, want 1: %q", n, got)
	}
	if !strings.Contains(lower, "epsilon") {
		t.Fatalf("relevant memory missing from reflection: %q", got)
	}
}

func TestBankReflectIsBounded(t *testing.T) {
	bank := memory.NewBank(t.TempDir())
	long := "alpha beta " + strings.Repeat("filler ", 800) + "omega"
	if _, err := bank.Retain(long, nil, ""); err != nil {
		t.Fatal(err)
	}
	got := bank.Reflect("alpha beta", 10)
	if !strings.HasPrefix(got, "Reflection (extractive synthesis):") {
		t.Fatalf("missing extractive header: %q", got)
	}
	if len(got) > 4096 || len(got) == 0 {
		t.Fatalf("reflection is %d bytes, want 1..4096", len(got))
	}
	if strings.Contains(got, strings.Repeat("filler ", 800)) {
		t.Fatal("oversized memory was not clipped to the output budget")
	}
}

func TestBankReflectEmptyBank(t *testing.T) {
	bank := memory.NewBank(t.TempDir())
	if got := bank.Reflect("anything", 10); got != "no memories to reflect on" {
		t.Fatalf("reflect = %q, want empty notice", got)
	}
}

func TestBankLearnPromoteWritesSkillFile(t *testing.T) {
	bank := memory.NewBank(t.TempDir())
	id, path, err := bank.Learn("always use gofmt before commit", []string{"go", "style"}, true)
	if err != nil {
		t.Fatalf("learn: %v", err)
	}
	if id == "" {
		t.Fatal("learn returned an empty id")
	}
	wantPath := filepath.Join(bank.Dir(), "skills", id+".md")
	if path != wantPath {
		t.Fatalf("skill path = %q, want %q", path, wantPath)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("skill file missing: %v", err)
	}
	content := string(data)
	for _, want := range []string{"---\n", "name: " + id, "description: go, style", "always use gofmt before commit"} {
		if !strings.Contains(content, want) {
			t.Fatalf("skill file missing %q:\n%s", want, content)
		}
	}
	if hits := bank.Recall("gofmt", nil, 5); len(hits) != 1 || hits[0].ID != id {
		t.Fatalf("lesson was not retained: %#v", hits)
	}
}

func TestBankLearnWithoutPromoteWritesNoSkill(t *testing.T) {
	bank := memory.NewBank(t.TempDir())
	id, path, err := bank.Learn("remember this lesson", nil, false)
	if err != nil || id == "" || path != "" {
		t.Fatalf("learn = (%q, %q, %v), want id and no path/error", id, path, err)
	}
	if _, err := os.Stat(filepath.Join(bank.Dir(), "skills", id+".md")); !os.IsNotExist(err) {
		t.Fatalf("skill file was written without promote: %v", err)
	}
}

func TestBankLearnSkillFailureStillRetainsLesson(t *testing.T) {
	dir := t.TempDir()
	// 强制受管技能目录创建失败：本应是目录的位置
	// 放了一个普通文件。
	if err := os.WriteFile(filepath.Join(dir, "skills"), []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	bank := memory.NewBank(dir)
	id, path, err := bank.Learn("lesson survives skill failure", nil, true)
	if err == nil {
		t.Fatal("learn should report the skill-write failure")
	}
	if id == "" || path != "" {
		t.Fatalf("learn = (%q, %q, %v), want retained id and empty path", id, path, err)
	}
	if hits := bank.Recall("lesson survives", nil, 5); len(hits) != 1 || hits[0].ID != id {
		t.Fatalf("lesson lost after skill failure: %#v", hits)
	}
}

func TestBankInvalidateExcludesFromRecallAndReflect(t *testing.T) {
	bank := memory.NewBank(t.TempDir())
	if _, err := bank.Retain("keep this fact", nil, ""); err != nil {
		t.Fatal(err)
	}
	dropID, err := bank.Retain("drop this stale fact", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := bank.Reflect("stale", 5); !strings.Contains(got, "stale fact") {
		t.Fatalf("pre-invalidation reflect = %q", got)
	}
	if err := bank.Invalidate(dropID, "outdated"); err != nil {
		t.Fatalf("invalidate: %v", err)
	}
	if got := bank.Recall("stale", nil, 5); len(got) != 0 {
		t.Fatalf("invalidated memory still in recall: %#v", got)
	}
	if got := bank.Recall("", nil, 10); len(got) != 1 || got[0].Text != "keep this fact" {
		t.Fatalf("recall = %#v, want only the kept memory", got)
	}
	if got := bank.Reflect("stale", 5); got != "no memories to reflect on" {
		t.Fatalf("invalidated memory still in reflect: %q", got)
	}
	if got := bank.Reflect("", 10); strings.Contains(got, "stale fact") || !strings.Contains(got, "keep this fact") {
		t.Fatalf("reflect = %q, want only the kept memory", got)
	}
	// 墓碑仍留在磁盘和 All 中，并带有其原因。
	if all := bank.All(); len(all) != 2 {
		t.Fatalf("all = %#v, want both records (one tombstone)", all)
	}
	data, err := os.ReadFile(filepath.Join(bank.Dir(), "memory.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"invalidated":"outdated"`) {
		t.Fatalf("invalidated field not persisted: %s", data)
	}
	if err := bank.Invalidate(dropID, "again"); err != nil {
		t.Fatalf("invalidate must be idempotent: %v", err)
	}
	if err := bank.Invalidate("unknown-id", "x"); err != nil {
		t.Fatalf("unknown id must be a no-op: %v", err)
	}
	if err := bank.Invalidate("  ", "x"); err == nil {
		t.Fatal("invalidate with empty id must fail")
	}
}

func TestMemoryAdvancedToolMetadata(t *testing.T) {
	tools := MemoryAdvancedTools(memory.NewBank(t.TempDir()))
	if len(tools) != 2 {
		t.Fatalf("got %d tools, want 2", len(tools))
	}
	if tools[0].Name() != "memory_reflect" || tools[0].Mutating() {
		t.Fatalf("tool[0] = %q mutating=%v, want read-only memory_reflect", tools[0].Name(), tools[0].Mutating())
	}
	if tools[1].Name() != "memory_learn" || !tools[1].Mutating() {
		t.Fatalf("tool[1] = %q mutating=%v, want mutating memory_learn", tools[1].Name(), tools[1].Mutating())
	}
	for _, tool := range tools {
		if tool.Description() == "" {
			t.Fatalf("%s has an empty description", tool.Name())
		}
		if !json.Valid([]byte(tool.Schema())) {
			t.Fatalf("%s schema is not valid JSON: %s", tool.Name(), tool.Schema())
		}
		if len(tool.Schema()) >= 1024 {
			t.Fatalf("%s schema is %d bytes, want < 1024", tool.Name(), len(tool.Schema()))
		}
	}
}

func TestMemoryReflectToolRun(t *testing.T) {
	bank := memory.NewBank(t.TempDir())
	if _, err := bank.Retain("golang testing patterns", nil, ""); err != nil {
		t.Fatal(err)
	}
	out, err := NewMemoryReflectTool(bank).Run(context.Background(), "", json.RawMessage(`{"query":"golang testing","limit":5}`))
	if err != nil {
		t.Fatalf("reflect run: %v", err)
	}
	if !strings.Contains(out, "golang testing patterns") {
		t.Fatalf("reflect output = %q", out)
	}
	empty, err := NewMemoryReflectTool(memory.NewBank(t.TempDir())).Run(context.Background(), "", json.RawMessage(`{"query":"anything"}`))
	if err != nil || empty != "no memories to reflect on" {
		t.Fatalf("empty reflect = (%q, %v)", empty, err)
	}
	if _, err := NewMemoryReflectTool(nil).Run(context.Background(), "", json.RawMessage(`{}`)); err == nil {
		t.Fatal("reflect with no bank must fail")
	}
}

func TestMemoryLearnToolRun(t *testing.T) {
	bank := memory.NewBank(t.TempDir())
	tool := NewMemoryLearnTool(bank)
	type result struct {
		ID        string `json:"id"`
		SkillPath string `json:"skillPath"`
	}
	out, err := tool.Run(context.Background(), "", json.RawMessage(`{"text":"use table tests","tags":["go"],"promote":false}`))
	if err != nil {
		t.Fatalf("learn run: %v", err)
	}
	var got result
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("learn output %q is not JSON: %v", out, err)
	}
	if got.ID == "" || got.SkillPath != "" {
		t.Fatalf("learn output = %#v, want id and empty skillPath", got)
	}
	promoted, err := tool.Run(context.Background(), "", json.RawMessage(`{"text":"promote me","promote":true}`))
	if err != nil {
		t.Fatalf("promoted learn run: %v", err)
	}
	var gotPromoted result
	if err := json.Unmarshal([]byte(promoted), &gotPromoted); err != nil {
		t.Fatalf("promoted learn output %q is not JSON: %v", promoted, err)
	}
	if gotPromoted.ID == "" || gotPromoted.SkillPath == "" {
		t.Fatalf("promoted learn output = %#v, want id and skillPath", gotPromoted)
	}
	if _, err := os.Stat(gotPromoted.SkillPath); err != nil {
		t.Fatalf("promoted skill file missing: %v", err)
	}
	if _, err := tool.Run(context.Background(), "", json.RawMessage(`{"text":""}`)); err == nil {
		t.Fatal("learn with empty text must fail")
	}
	if _, err := NewMemoryLearnTool(nil).Run(context.Background(), "", json.RawMessage(`{"text":"x"}`)); err == nil {
		t.Fatal("learn with no bank must fail")
	}
}
