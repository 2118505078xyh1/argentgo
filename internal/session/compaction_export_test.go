package session

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
)

// exportRaw 构建一个 JSON 对象载荷，不附加任何保留提示。
func exportRaw(t *testing.T, obj map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// exportMarked 在载荷上附加保留提示。
func exportMarked(t *testing.T, raw json.RawMessage, r Retention) json.RawMessage {
	t.Helper()
	data, err := SetRetention(raw, r)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// exportToolData 构建一条工具结果的载荷（可带 ToolCallID 与保留提示）。
func exportToolData(t *testing.T, toolCallID, content string, r Retention, mark bool) json.RawMessage {
	t.Helper()
	obj := map[string]any{"content": content}
	if toolCallID != "" {
		obj["toolCallId"] = toolCallID
	}
	raw := exportRaw(t, obj)
	if !mark {
		return raw
	}
	return exportMarked(t, raw, r)
}

// exportAssistantData 构建一条 assistant 载荷，携带 tool_calls 声明以便配对。
func exportAssistantData(t *testing.T, content, callID string, r Retention, mark bool) json.RawMessage {
	t.Helper()
	obj := map[string]any{
		"content":    content,
		"tool_calls": []map[string]any{{"id": callID}},
	}
	raw := exportRaw(t, obj)
	if !mark {
		return raw
	}
	return exportMarked(t, raw, r)
}

// exportedContents 提取保留记录中的 content，便于表驱动断言。
func exportedContents(records []Record) []string {
	out := make([]string, 0, len(records))
	for _, record := range records {
		out = append(out, fieldString(record.Data, "content"))
	}
	return out
}

func exportedKinds(records []Record) []string {
	out := make([]string, 0, len(records))
	for _, record := range records {
		out = append(out, record.Kind)
	}
	return out
}

// flaggedSession 用原始记录构造一个会话，使 Records()/activePath 完全由测试控制。
func flaggedSession(t *testing.T, records []Record, active string) *Session {
	t.Helper()
	dir := t.TempDir()
	id := "flagged"
	writeRawSession(t, filepath.Join(dir, id+fileExt), records)
	return &Session{dir: dir, id: id, active: active}
}

// TestRetainedForCompaction 覆盖薄导出的保留筛选与工具配对行为。
func TestRetainedForCompaction(t *testing.T) {
	tests := []struct {
		name         string
		entries      func(t *testing.T) []Entry
		wantContents []string
		wantKinds    []string
	}{
		{
			name: "marked tool result survives and pulls its call",
			entries: func(t *testing.T) []Entry {
				return []Entry{
					{ID: "a1", Role: "assistant", Data: exportAssistantData(t, "ask", "call-1", RetainDefault, false)},
					{ID: "t1", Role: "tool", ToolCallID: "call-1", Data: exportToolData(t, "call-1", "result", RetainAlways, true)},
				}
			},
			wantContents: []string{"ask", "result"},
			wantKinds:    []string{KindAssistant, KindTool},
		},
		{
			name: "unmarked records are not retained",
			entries: func(t *testing.T) []Entry {
				return []Entry{
					{ID: "a1", Role: "assistant", Data: exportAssistantData(t, "ask", "call-1", RetainDefault, false)},
					{ID: "t1", Role: "tool", ToolCallID: "call-1", Data: exportToolData(t, "call-1", "result", RetainDefault, false)},
				}
			},
			wantContents: []string{},
			wantKinds:    []string{},
		},
		{
			name: "in_thread survives compaction",
			entries: func(t *testing.T) []Entry {
				return []Entry{{ID: "t1", Role: "tool", ToolCallID: "call-1", Data: exportToolData(t, "call-1", "thread result", RetainInThread, true)}}
			},
			wantContents: []string{"thread result"},
			wantKinds:    []string{KindTool},
		},
		{
			name: "in_children does not survive compaction",
			entries: func(t *testing.T) []Entry {
				return []Entry{{ID: "t1", Role: "tool", ToolCallID: "call-1", Data: exportToolData(t, "call-1", "child result", RetainInChildren, true)}}
			},
			wantContents: []string{},
			wantKinds:    []string{},
		},
		{
			name: "marked assistant pulls unmarked result",
			entries: func(t *testing.T) []Entry {
				return []Entry{
					{ID: "a1", Role: "assistant", Data: exportAssistantData(t, "ask", "call-1", RetainAlways, true)},
					{ID: "t1", Role: "tool", ToolCallID: "call-1", Data: exportToolData(t, "call-1", "result", RetainDefault, false)},
				}
			},
			wantContents: []string{"ask", "result"},
			wantKinds:    []string{KindAssistant, KindTool},
		},
		{
			name: "CJK content preserved verbatim",
			entries: func(t *testing.T) []Entry {
				return []Entry{{ID: "t1", Role: "tool", ToolCallID: "工具-1", Data: exportToolData(t, "工具-1", "保留此结果 — 中文内容", RetainAlways, true)}}
			},
			wantContents: []string{"保留此结果 — 中文内容"},
			wantKinds:    []string{KindTool},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RetainedForCompaction(tt.entries(t))
			if !reflect.DeepEqual(exportedContents(got), tt.wantContents) {
				t.Fatalf("contents = %v, want %v", exportedContents(got), tt.wantContents)
			}
			if !reflect.DeepEqual(exportedKinds(got), tt.wantKinds) {
				t.Fatalf("kinds = %v, want %v", exportedKinds(got), tt.wantKinds)
			}
			// Data 必须逐字保留，确保投影时不丢字段。
			for _, record := range got {
				if !containsString(tt.wantContents, fieldString(record.Data, "content")) {
					t.Fatalf("unexpected record content: %s", record.Data)
				}
			}
		})
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// TestFlaggedToolResults 覆盖标记识别、未标记排除、角色与 id 过滤、重试取最后一条与 CJK。
func TestFlaggedToolResults(t *testing.T) {
	root := Record{ID: "n1", Kind: KindHeader, Data: json.RawMessage(`{"title":"flagged"}`)}

	tests := []struct {
		name  string
		build func(t *testing.T) *Session
		want  map[string]string
	}{
		{
			name: "marked tool result recognized",
			build: func(t *testing.T) *Session {
				return flaggedSession(t, []Record{
					root,
					{ID: "t1", Parent: "n1", Kind: KindTool, Data: exportToolData(t, "call-1", "result A", RetainAlways, true)},
				}, "t1")
			},
			want: map[string]string{"call-1": "result A"},
		},
		{
			name: "unmarked tool result not recognized",
			build: func(t *testing.T) *Session {
				return flaggedSession(t, []Record{
					root,
					{ID: "t1", Parent: "n1", Kind: KindTool, Data: exportToolData(t, "call-1", "result A", RetainDefault, false)},
				}, "t1")
			},
			want: map[string]string{},
		},
		{
			name: "in_children tool result not recognized",
			build: func(t *testing.T) *Session {
				return flaggedSession(t, []Record{
					root,
					{ID: "t1", Parent: "n1", Kind: KindTool, Data: exportToolData(t, "call-1", "child result", RetainInChildren, true)},
				}, "t1")
			},
			want: map[string]string{},
		},
		{
			name: "assistant carrying toolCallId excluded",
			build: func(t *testing.T) *Session {
				data := exportMarked(t, exportRaw(t, map[string]any{
					"content": "ask", "toolCallId": "call-x",
				}), RetainAlways)
				return flaggedSession(t, []Record{
					root,
					{ID: "a1", Parent: "n1", Kind: KindAssistant, Data: data},
				}, "a1")
			},
			want: map[string]string{},
		},
		{
			name: "tool result without tool call id excluded",
			build: func(t *testing.T) *Session {
				return flaggedSession(t, []Record{
					root,
					{ID: "t1", Parent: "n1", Kind: KindTool, Data: exportToolData(t, "", "no id", RetainAlways, true)},
				}, "t1")
			},
			want: map[string]string{},
		},
		{
			name: "same id retry keeps last result",
			build: func(t *testing.T) *Session {
				return flaggedSession(t, []Record{
					root,
					{ID: "t1", Parent: "n1", Kind: KindTool, Data: exportToolData(t, "call-1", "first", RetainAlways, true)},
					{ID: "t2", Parent: "t1", Kind: KindTool, Data: exportToolData(t, "call-1", "second", RetainAlways, true)},
				}, "t2")
			},
			want: map[string]string{"call-1": "second"},
		},
		{
			name: "CJK content preserved",
			build: func(t *testing.T) *Session {
				return flaggedSession(t, []Record{
					root,
					{ID: "t1", Parent: "n1", Kind: KindTool, Data: exportToolData(t, "call-cjk", "中文结果 — 保留", RetainInThread, true)},
				}, "t1")
			},
			want: map[string]string{"call-cjk": "中文结果 — 保留"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := tt.build(t)
			got := s.FlaggedToolResults()
			if len(got) != len(tt.want) {
				t.Fatalf("FlaggedToolResults() = %v, want %v", got, tt.want)
			}
			for id, wantContent := range tt.want {
				record, ok := got[id]
				if !ok {
					t.Fatalf("missing tool call id %q in %v", id, got)
				}
				if content := fieldString(record.Data, "content"); content != wantContent {
					t.Fatalf("id %q content = %q, want %q", id, content, wantContent)
				}
			}
		})
	}
}

// TestFlaggedToolResultsActiveBranch 验证只读取活动分支上的标记结果。
func TestFlaggedToolResultsActiveBranch(t *testing.T) {
	records := []Record{
		{ID: "n1", Kind: KindHeader, Data: json.RawMessage(`{"title":"branch"}`)},
		{ID: "a1", Parent: "n1", Kind: KindAssistant, Data: exportAssistantData(t, "ask", "call-active", RetainDefault, false)},
		{ID: "t-active", Parent: "a1", Kind: KindTool, Data: exportToolData(t, "call-active", "active result", RetainAlways, true)},
		{ID: "t-inactive", Parent: "n1", Kind: KindTool, Data: exportToolData(t, "call-inactive", "inactive result", RetainAlways, true)},
	}
	tests := []struct {
		name   string
		active string
		want   map[string]string
	}{
		{"active is active branch", "t-active", map[string]string{"call-active": "active result"}},
		{"active is inactive branch", "t-inactive", map[string]string{"call-inactive": "inactive result"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := flaggedSession(t, records, tt.active)
			got := s.FlaggedToolResults()
			if len(got) != len(tt.want) {
				t.Fatalf("FlaggedToolResults() = %v, want %v", got, tt.want)
			}
			for id, wantContent := range tt.want {
				record, ok := got[id]
				if !ok || fieldString(record.Data, "content") != wantContent {
					t.Fatalf("id %q = %v, want content %q", id, record, wantContent)
				}
			}
			if _, leaked := got["call-inactive"]; tt.active == "t-active" && leaked {
				t.Fatalf("inactive branch result leaked into %v", got)
			}
			if _, leaked := got["call-active"]; tt.active == "t-inactive" && leaked {
				t.Fatalf("inactive branch result leaked into %v", got)
			}
		})
	}
}

// TestFlaggedToolResultsDoesNotModifySession 验证该薄导出全程只读。
func TestFlaggedToolResultsDoesNotModifySession(t *testing.T) {
	root := Record{ID: "n1", Kind: KindHeader, Data: json.RawMessage(`{"title":"readonly"}`)}
	tests := []struct {
		name    string
		records []Record
		active  string
	}{
		{
			name: "with flagged result",
			records: []Record{
				root,
				{ID: "t1", Parent: "n1", Kind: KindTool, Data: exportToolData(t, "call-1", "result", RetainAlways, true)},
			},
			active: "t1",
		},
		{
			name: "without flagged result",
			records: []Record{
				root,
				{ID: "t1", Parent: "n1", Kind: KindTool, Data: exportToolData(t, "call-1", "result", RetainDefault, false)},
			},
			active: "t1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := flaggedSession(t, tt.records, tt.active)
			before, err := s.Records()
			if err != nil {
				t.Fatal(err)
			}
			beforeActive := s.activeSnapshot()
			_ = s.FlaggedToolResults()
			after, err := s.Records()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("Records() changed: before=%+v after=%+v", before, after)
			}
			if s.activeSnapshot() != beforeActive {
				t.Fatalf("active changed: before=%q after=%q", beforeActive, s.activeSnapshot())
			}
		})
	}
}

// TestEntryFromRecord 验证薄封装忠实委托 messageEntry 的投影规则。
func TestEntryFromRecord(t *testing.T) {
	tests := []struct {
		name        string
		record      func(t *testing.T) Record
		wantOK      bool
		wantRole    string
		wantContent string
		wantID      string
	}{
		{
			name: "tool record",
			record: func(t *testing.T) Record {
				return Record{ID: "t1", Kind: KindTool, Data: exportMarked(t, exportToolData(t, "call-1", "result", RetainAlways, false), RetainAlways)}
			},
			wantOK: true, wantRole: "tool", wantContent: "result", wantID: "t1",
		},
		{
			name: "user record",
			record: func(t *testing.T) Record {
				return Record{ID: "u1", Kind: KindUser, Data: json.RawMessage(`"hello"`)}
			},
			wantOK: true, wantRole: "user", wantContent: "hello", wantID: "u1",
		},
		{
			name: "assistant record with tool call id",
			record: func(t *testing.T) Record {
				return Record{ID: "a1", Kind: KindAssistant, Data: exportRaw(t, map[string]any{
					"content": "ask", "toolCallId": "call-1",
				})}
			},
			wantOK: true, wantRole: "assistant", wantContent: "ask", wantID: "a1",
		},
		{
			name: "summary record uses summary key",
			record: func(t *testing.T) Record {
				return Record{ID: "s1", Kind: KindSummary, Data: exportRaw(t, map[string]any{"summary": "kept"})}
			},
			wantOK: true, wantRole: "system", wantContent: "kept", wantID: "s1",
		},
		{
			name: "unknown kind rejected",
			record: func(t *testing.T) Record {
				return Record{ID: "x1", Kind: KindCompaction, Data: exportRaw(t, map[string]any{"summary": "x"})}
			},
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry, ok := EntryFromRecord(tt.record(t))
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (entry=%+v)", ok, tt.wantOK, entry)
			}
			if !tt.wantOK {
				return
			}
			if entry.ID != tt.wantID || entry.Role != tt.wantRole || entry.Content != tt.wantContent {
				t.Fatalf("entry = %+v, want id=%q role=%q content=%q", entry, tt.wantID, tt.wantRole, tt.wantContent)
			}
		})
	}
}
