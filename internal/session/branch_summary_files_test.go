package session

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestBranchSummaryFiles 验证遗弃分支摘要机械抽取 read/modified 文件清单，
// 并能跨嵌套摘要累计；清单去重且只含路径，不含工具原文或敏感参数。
func TestBranchSummaryFiles(t *testing.T) {
	dir := t.TempDir()
	id := "branch-files"
	path := filepath.Join(dir, id+fileExt)
	ts := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
	toolCalls := json.RawMessage(`{"tool_calls":[` +
		`{"id":"c1","function":{"name":"read","arguments":"{\"path\":\"src/a.go\"}"}},` +
		`{"id":"c2","function":{"name":"edit","arguments":"{\"file_path\":\"src/b.go\"}"}},` +
		`{"id":"c3","function":{"name":"read","arguments":"{\"path\":\"src/a.go\"}"}}` +
		`]}`)
	records := []Record{
		{ID: "n000001", Kind: KindHeader, TS: ts, Data: json.RawMessage(`{"title":"t","created":"2026-11-23T08:00:00Z"}`)},
		{ID: "n000002", Parent: "n000001", Kind: KindUser, TS: ts, Data: json.RawMessage(`{"content":"fork point"}`)},
		{ID: "n000003", Parent: "n000002", Kind: KindAssistant, TS: ts, Data: json.RawMessage(`{"content":"abandoned work"}`)},
		{ID: "n000004", Parent: "n000003", Kind: KindAssistant, TS: ts, Data: toolCalls},
		{ID: "n000005", Parent: "n000002", Kind: KindAssistant, TS: ts, Data: json.RawMessage(`{"content":"active branch"}`)},
	}
	writeBranchSummaryRecords(t, path, records)

	summary, err := SummarizeAbandonedBranch(path)
	if err != nil {
		t.Fatalf("SummarizeAbandonedBranch: %v", err)
	}
	text := branchSummaryText(t, summary)
	if !strings.Contains(text, "[read: src/a.go]") {
		t.Fatalf("summary missing read list: %q", text)
	}
	if !strings.Contains(text, "[modified: src/b.go]") {
		t.Fatalf("summary missing modified list: %q", text)
	}
	if strings.Count(text, "[read: src/a.go]") != 1 || strings.Count(text, "[modified: src/b.go]") != 1 {
		t.Fatalf("file list was not deduped: %q", text)
	}

	// 跨嵌套累计：分支上已有的摘要清单应并入新的摘要。
	nested := json.RawMessage(`{"text":"older summary","branch_files":` +
		`{"read":["src/old.go"],"modified":["src/b.go","src/old_mod.go"]}}`)
	records = append(records[:4], append([]Record{
		{ID: "n000006", Parent: "n000004", Kind: KindSummary, TS: ts, Data: nested},
	}, records[4:]...)...)
	writeBranchSummaryRecords(t, path, records)
	summary, err = SummarizeAbandonedBranch(path)
	if err != nil {
		t.Fatalf("SummarizeAbandonedBranch (nested): %v", err)
	}
	text = branchSummaryText(t, summary)
	for _, want := range []string{"[read: src/a.go, src/old.go]", "[modified: src/b.go, src/old_mod.go]"} {
		if !strings.Contains(text, want) {
			t.Fatalf("summary missing nested accumulation %q: %q", want, text)
		}
	}
	if strings.Count(text, "[modified: src/b.go, src/old_mod.go]") != 1 {
		t.Fatalf("nested modified path was not deduped: %q", text)
	}
}
