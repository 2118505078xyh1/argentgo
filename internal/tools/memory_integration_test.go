package tools

// End-to-end coverage for the persistent memory bank: every assertion goes
// through Registry.Run so the real guard/yolo/context wiring is exercised,
// not just the tool structs in isolation. The bank is a real temp-dir bank
// injected with SetMemoryBank, so all five memory_* tools share it.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/memory"
)

// memoryIntegrationRun calls a tool the way the agent loop does and fails on a
// tool error, so a passing assertion always means the full Run path succeeded.
func memoryIntegrationRun(t *testing.T, r *Registry, name, args string) string {
	t.Helper()
	out, err := r.Run(context.Background(), name, args)
	if err != nil {
		t.Fatalf("Run(%s, %s): %v", name, args, err)
	}
	return out
}

// memoryIntegrationJSON encodes tool arguments without hand-escaping strings
// that may contain secret-shaped text.
func memoryIntegrationJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("encode args: %v", err)
	}
	return string(data)
}

// memoryIntegrationHits decodes the JSON array emitted by memory_recall and
// checks it is the expected kind of result (not an "unavailable" notice).
func memoryIntegrationHits(t *testing.T, out string) []memory.Memory {
	t.Helper()
	var hits []memory.Memory
	if err := json.Unmarshal([]byte(out), &hits); err != nil {
		t.Fatalf("recall output is not a memory list: %v (%s)", err, out)
	}
	return hits
}

// Scenario A: capture three tagged facts, recall the right one by keyword,
// reflect over another, forget it, then invalidate a third. All observations
// are read back through Registry.Run against the same real bank.
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

	// Keyword recall must select the runbook fact and nothing else.
	out := memoryIntegrationRun(t, registry, "memory_recall", `{"query":"runbook","limit":5}`)
	hits := memoryIntegrationHits(t, out)
	if len(hits) != 1 || hits[0].ID != ids[0] || hits[0].Text != runbook {
		t.Fatalf("recall(runbook) = %#v, want only the runbook fact", hits)
	}
	if !strings.Contains(out, runbook) {
		t.Fatalf("recall output = %q, want the retained text", out)
	}

	// Reflect is extractive: the synthesis must contain the retained text of
	// the fact most relevant to the query.
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

	// Forget removes the fact from the bank; recall can no longer find it.
	out = memoryIntegrationRun(t, registry, "memory_forget", memoryIntegrationJSON(t, map[string]any{"id": ids[1]}))
	if out != "forgot memory "+ids[1] {
		t.Fatalf("forget output = %q, want %q", out, "forgot memory "+ids[1])
	}
	out = memoryIntegrationRun(t, registry, "memory_recall", `{"query":"backup","limit":5}`)
	if out != "no memories found" {
		t.Fatalf("recall after forget = %q, want no memories found", out)
	}

	// There is no memory_invalidate tool, so invalidation is applied to the
	// very bank the registry tools read from. Exclusion is then verified
	// through Registry.Run, which is the behaviour under test.
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

	// Invalidation is a tombstone, not a delete: the record stays on disk, so
	// All still has the invalidated fact plus the one untouched fact.
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

	// The fact that was neither forgotten nor invalidated is still reachable.
	out = memoryIntegrationRun(t, registry, "memory_recall", `{"query":"approval","limit":5}`)
	hits = memoryIntegrationHits(t, out)
	if len(hits) != 1 || hits[0].ID != ids[2] || hits[0].Text != release {
		t.Fatalf("recall(approval) = %#v, want the release fact", hits)
	}
}

// Scenario B: memory_learn with promote=true writes a SKILL.md-compatible file
// under the bank directory, and the lesson is still found by memory_recall.
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

// Scenario C: a credential-shaped key never reaches the JSONL log and is never
// echoed by recall, even though the surrounding fact is stored normally.
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

// Scenario D: swapping the bank behind the tools moves subsequent writes to the
// new directory and hides facts from the previous bank.
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
