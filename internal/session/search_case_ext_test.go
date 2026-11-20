package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSearchStripsUppercaseJSONLExtension 覆盖一个大小写不一致的 bug：
// Search 以不区分大小写的方式匹配 ".jsonl" 后缀，却用
// 区分大小写的 strings.TrimSuffix 派生会话 id。因此手工复制的
// 名为 "SESSION.JSONL" 的转录会产生 Hit.ID "SESSION.JSONL"，
// 再也无法通过 Store.OpenSession 解析。必须按长度剥离 id，
// 与 Container.List 一样，并保留原始大小写。
func TestSearchStripsUppercaseJSONLExtension(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"UPPER.JSONL", "Mixed.Jsonl", "lower.jsonl"} {
		record := Record{
			ID:   "n000001",
			Kind: KindUser,
			Data: json.RawMessage(`{"role":"user","content":"needle in ` + name + `"}`),
		}
		line, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), append(line, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	hits, err := Search(dir, "needle", 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 3 {
		t.Fatalf("hits = %d, want 3", len(hits))
	}
	wantIDs := map[string]bool{"UPPER": true, "Mixed": true, "lower": true}
	for _, hit := range hits {
		if !wantIDs[hit.ID] {
			t.Errorf("hit ID = %q, want one of %v", hit.ID, wantIDs)
		}
		if strings.EqualFold(hit.ID, "UPPER") && hit.ID != "UPPER" {
			t.Errorf("case not preserved: %q", hit.ID)
		}
	}
}
