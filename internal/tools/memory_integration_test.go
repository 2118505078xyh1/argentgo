package tools

// 对持久化记忆库的端到端覆盖：每条断言都经过
// Registry.Run，因此走的是真实的守卫/yolo/上下文接线，
// 而不只是孤立的工具结构体。记忆库是真实的临时目录库，
// 通过 SetMemoryBank 注入，因此五个 memory_* 工具共享它。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"example.com/argentgo/internal/memory"
)

// memoryIntegrationRun 以 agent 循环的方式调用工具，并在
// 工具出错时失败，因此断言通过始终意味着完整的 Run 路径成功。
func memoryIntegrationRun(t *testing.T, r *Registry, name, args string) string {
	t.Helper()
	out, err := r.Run(context.Background(), name, args)
	if err != nil {
		t.Fatalf("Run(%s, %s): %v", name, args, err)
	}
	return out
}

// memoryIntegrationJSON 编码工具参数，无需对可能包含
// 类似密钥文本的字符串手工转义。
func memoryIntegrationJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("encode args: %v", err)
	}
	return string(data)
}

// memoryIntegrationHits 解析 memory_recall 发出的 JSON 数组，
// 并检查它是预期的结果类型（而非 "unavailable" 通知）。
func memoryIntegrationHits(t *testing.T, out string) []memory.Memory {
	t.Helper()
	var hits []memory.Memory
	if err := json.Unmarshal([]byte(out), &hits); err != nil {
		t.Fatalf("recall output is not a memory list: %v (%s)", err, out)
	}
	return hits
}

// 场景 A：捕获三条带标签的事实，通过关键词回忆出正确的一条，
// 对另一条进行 reflect，遗忘它，再使第三条失效。所有观察
// 都通过 Registry.Run 针对同一个真实库读回。
func TestMemoryIntegrationCaptureRecallReflectForgetInvalidate(t *testing.T) {
	dir := t.TempDir()
	bank := memory.NewBank(dir)
	registry := New(t.TempDir(), true) // mutating memory tools need yolo
	registry.SetMemoryBank(bank)

	const (
		runbook = "The deployment runbook lives in docs/deploy.md"
		backup  = "The database backup window is 02:00 UTC"
		release = "Alice owns the release approval"
	)
	facts := []struct {
		text string
		tags []string
	}{
		{runbook, []string{"ops", "runbook"}},
		{backup, []string{"ops", "database"}},
		{release, []string{"people", "release"}},
	}

	ids := make([]string, 0, len(facts))
	for _, fact := range facts {
		out := memoryIntegrationRun(t, registry, "memory_retain", memoryIntegrationJSON(t, map[string]any{
			"text":   fact.text,
			"tags":   fact.tags,
			"source": "integration test",
		}))
		id := strings.TrimPrefix(out, "stored memory ")
		if id == out || id == "" {
			t.Fatalf("retain output = %q, want 'stored memory <id>'", out)
		}
		ids = append(ids, id)
	}
	if len(ids) != 3 || ids[0] == ids[1] || ids[1] == ids[2] {
		t.Fatalf("retained ids are not distinct: %v", ids)
	}

	// 关键词回忆必须选出 runbook 那条事实，且仅此一条。
	out := memoryIntegrationRun(t, registry, "memory_recall", `{"query":"runbook","limit":5}`)
	hits := memoryIntegrationHits(t, out)
	if len(hits) != 1 || hits[0].ID != ids[0] || hits[0].Text != runbook {
		t.Fatalf("recall(runbook) = %#v, want only the runbook fact", hits)
	}
	if !strings.Contains(out, runbook) {
		t.Fatalf("recall output = %q, want the retained text", out)
	}

	// Reflect 是抽取式的：综合结果必须包含与查询最相关的
	// 那条事实的保留文本。
	out = memoryIntegrationRun(t, registry, "memory_reflect", `{"query":"backup","limit":5}`)
	if !strings.HasPrefix(out, "Reflection (extractive synthesis):") {
		t.Fatalf("reflect output has no extractive header: %q", out)
	}
	if !strings.Contains(out, backup) {
		t.Fatalf("reflect output = %q, want the backup fact", out)
	}
	if strings.Contains(out, runbook) {
		t.Fatalf("reflect output = %q, want only relevant memories", out)
	}

	// Forget 会从库中移除该事实；回忆再也找不到它。
	out = memoryIntegrationRun(t, registry, "memory_forget", memoryIntegrationJSON(t, map[string]any{"id": ids[1]}))
	if out != "forgot memory "+ids[1] {
		t.Fatalf("forget output = %q, want %q", out, "forgot memory "+ids[1])
	}
	out = memoryIntegrationRun(t, registry, "memory_recall", `{"query":"backup","limit":5}`)
	if out != "no memories found" {
		t.Fatalf("recall after forget = %q, want no memories found", out)
	}

	// 没有 memory_invalidate 工具，因此失效直接应用于
	// 注册表工具所读取的同一个库。随后通过 Registry.Run
	// 验证排除效果，这正是被测行为。
	if err := bank.Invalidate(ids[0], "superseded by newer runbook"); err != nil {
		t.Fatalf("invalidate: %v", err)
	}
	out = memoryIntegrationRun(t, registry, "memory_recall", `{"query":"runbook","limit":5}`)
	if out != "no memories found" {
		t.Fatalf("recall after invalidate = %q, want no memories found", out)
	}
	out = memoryIntegrationRun(t, registry, "memory_reflect", `{"query":"runbook","limit":5}`)
	if out != "no memories to reflect on" {
		t.Fatalf("reflect after invalidate = %q, want empty notice", out)
	}

	// 失效是墓碑标记，而非删除：记录仍留在磁盘上，因此
	// All 仍包含已失效的事实加那条未改动的事实。
	if all := bank.All(); len(all) != 2 {
		t.Fatalf("All() after forget+invalidate = %#v, want 2 records", all)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "memory.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"invalidated":"superseded by newer runbook"`) {
		t.Fatalf("invalidation reason not persisted: %s", raw)
	}

	// 既未被遗忘也未被失效的事实仍然可达。
	out = memoryIntegrationRun(t, registry, "memory_recall", `{"query":"approval","limit":5}`)
	hits = memoryIntegrationHits(t, out)
	if len(hits) != 1 || hits[0].ID != ids[2] || hits[0].Text != release {
		t.Fatalf("recall(approval) = %#v, want the release fact", hits)
	}
}

// 场景 B：promote=true 的 memory_learn 会在库目录下写入一个
// 兼容 SKILL.md 的文件，且该经验仍能被 memory_recall 找到。
func TestMemoryIntegrationLearnPromotesSkillAndIsRecalled(t *testing.T) {
	dir := t.TempDir()
	bank := memory.NewBank(dir)
	registry := New(t.TempDir(), true)
	registry.SetMemoryBank(bank)

	const lesson = "Always run gofmt and go vet before committing Go changes"
	out := memoryIntegrationRun(t, registry, "memory_learn", memoryIntegrationJSON(t, map[string]any{
		"text":    lesson,
		"tags":    []string{"go", "style"},
		"promote": true,
	}))
	var promoted struct {
		ID        string `json:"id"`
		SkillPath string `json:"skillPath"`
	}
	if err := json.Unmarshal([]byte(out), &promoted); err != nil {
		t.Fatalf("learn output %q is not JSON: %v", out, err)
	}
	if promoted.ID == "" || promoted.SkillPath == "" {
		t.Fatalf("learn output = %q, want an id and a skill path", out)
	}
	wantPath := filepath.Join(dir, "skills", promoted.ID+".md")
	if promoted.SkillPath != wantPath {
		t.Fatalf("skill path = %q, want %q", promoted.SkillPath, wantPath)
	}

	data, err := os.ReadFile(promoted.SkillPath)
	if err != nil {
		t.Fatalf("promoted skill file missing: %v", err)
	}
	content := string(data)
	for _, want := range []string{
		"---\n",
		"name: " + promoted.ID,
		"description: go, style",
		lesson,
		"\n---\n\n", // closing frontmatter delimiter
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("skill file missing %q:\n%s", want, content)
		}
	}

	out = memoryIntegrationRun(t, registry, "memory_recall", `{"query":"gofmt vet","limit":5}`)
	hits := memoryIntegrationHits(t, out)
	if len(hits) != 1 || hits[0].ID != promoted.ID || hits[0].Text != lesson {
		t.Fatalf("recall(learned fact) = %#v, want the promoted lesson", hits)
	}
}

// 场景 C：形如凭据的键永远不会进入 JSONL 日志，也不会被
// 回忆回显，即使周围的事实被正常存储。
func TestMemoryIntegrationSecretNeverPersistedOrRecalled(t *testing.T) {
	dir := t.TempDir()
	bank := memory.NewBank(dir)
	registry := New(t.TempDir(), true)
	registry.SetMemoryBank(bank)

	const secret = "AKIAIOSFODNN7EXAMPLE"
	text := "the staging bucket uses " + secret + " for uploads"
	out := memoryIntegrationRun(t, registry, "memory_retain", memoryIntegrationJSON(t, map[string]any{
		"text": text,
		"tags": []string{"deploy"},
	}))
	if !strings.HasPrefix(out, "stored memory ") {
		t.Fatalf("retain output = %q, want a stored id", out)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "memory.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatalf("raw memory log leaked the AWS key: %s", raw)
	}
	if !strings.Contains(string(raw), "[redacted:aws key]") {
		t.Fatalf("raw memory log has no redaction placeholder: %s", raw)
	}

	out = memoryIntegrationRun(t, registry, "memory_recall", `{"query":"staging bucket","limit":5}`)
	if strings.Contains(out, secret) {
		t.Fatalf("recall echoed the AWS key: %s", out)
	}
	if !strings.Contains(out, "staging bucket") {
		t.Fatalf("recall did not find the non-secret part of the fact: %s", out)
	}
}

// 场景 D：交换工具背后的库会将后续写入移到
// 新目录，并隐藏旧库中的事实。
func TestMemoryIntegrationSetMemoryBankRedirectsWrites(t *testing.T) {
	bank1 := memory.NewBank(t.TempDir())
	bank2 := memory.NewBank(t.TempDir())
	if bank1.Dir() == bank2.Dir() {
		t.Fatal("test banks share a directory")
	}
	registry := New(t.TempDir(), true)
	registry.SetMemoryBank(bank1)

	memoryIntegrationRun(t, registry, "memory_retain", `{"text":"first bank fact"}`)
	path1 := filepath.Join(bank1.Dir(), "memory.jsonl")
	data1, err := os.ReadFile(path1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data1), "first bank fact") {
		t.Fatalf("first bank file = %q", data1)
	}

	registry.SetMemoryBank(bank2)
	memoryIntegrationRun(t, registry, "memory_retain", `{"text":"second bank fact"}`)
	path2 := filepath.Join(bank2.Dir(), "memory.jsonl")
	data2, err := os.ReadFile(path2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data2), "second bank fact") {
		t.Fatalf("second bank file = %q", data2)
	}

	data1Again, err := os.ReadFile(path1)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data1Again), "second bank fact") {
		t.Fatalf("override wrote into the first bank: %s", data1Again)
	}

	out := memoryIntegrationRun(t, registry, "memory_recall", `{"query":"bank","limit":10}`)
	hits := memoryIntegrationHits(t, out)
	if len(hits) != 1 || hits[0].Text != "second bank fact" {
		t.Fatalf("recall after override = %#v, want only the second bank's fact", hits)
	}
}
