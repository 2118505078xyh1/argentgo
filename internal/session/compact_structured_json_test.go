package session

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func fixedMeta() *Meta {
	return &Meta{
		ID:      "session-01",
		Title:   "lexer 重构",
		Updated: time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC),
		Turns:   7,
	}
}

func fullStructuredSummary() StructuredSummary {
	return StructuredSummary{
		Progress:      []string{"built lexer", "added tests"},
		Uncertainties: []string{"windows build fails"},
		UserActions:   []string{"review patch"},
	}
}

// TestStructuredSummaryJSONStructure 验证稳定 JSON 的字段集合与取值。
func TestStructuredSummaryJSONStructure(t *testing.T) {
	got := structuredSummaryJSON(fixedMeta(), fullStructuredSummary())
	if len(got) == 0 {
		t.Fatal("structuredSummaryJSON returned empty bytes for a non-empty summary")
	}

	var d structuredSummaryJSONDTO
	if err := json.Unmarshal(got, &d); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, got)
	}
	if d.ID != "session-01" || d.Title != "lexer 重构" || d.Turns != 7 {
		t.Errorf("metadata mismatch: %+v", d)
	}
	if want := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC); !d.Updated.Equal(want) {
		t.Errorf("Updated = %v, want %v", d.Updated, want)
	}
	if want := []string{"built lexer", "added tests"}; !reflect.DeepEqual(d.Progress, want) {
		t.Errorf("Progress = %q, want %q", d.Progress, want)
	}
	if want := []string{"windows build fails"}; !reflect.DeepEqual(d.Uncertainties, want) {
		t.Errorf("Uncertainties = %q, want %q", d.Uncertainties, want)
	}
	if want := []string{"review patch"}; !reflect.DeepEqual(d.UserActions, want) {
		t.Errorf("UserActions = %q, want %q", d.UserActions, want)
	}
}

// TestStructuredSummaryJSONThreeBuckets 验证三个桶都作为顶层字段出现，
// 且即使某个桶没有条目也输出 [] 而不是 null 或被省略。
func TestStructuredSummaryJSONThreeBuckets(t *testing.T) {
	got := structuredSummaryJSON(fixedMeta(), StructuredSummary{
		Progress: []string{"only progress"},
	})
	var m map[string]json.RawMessage
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	for _, key := range []string{"progress", "uncertainties", "userActions"} {
		raw, ok := m[key]
		if !ok {
			t.Errorf("missing bucket %q in %s", key, got)
			continue
		}
		var items []string
		if err := json.Unmarshal(raw, &items); err != nil {
			t.Errorf("bucket %q is not a JSON array: %s", key, raw)
		}
	}
	if raw := m["uncertainties"]; strings.TrimSpace(string(raw)) != "[]" {
		t.Errorf("empty bucket should be [], got %s", raw)
	}
	if raw := m["userActions"]; strings.TrimSpace(string(raw)) != "[]" {
		t.Errorf("empty bucket should be [], got %s", raw)
	}
}

// TestStructuredSummaryJSONCJK 验证中文内容原样输出，不转义成 \uXXXX。
func TestStructuredSummaryJSONCJK(t *testing.T) {
	summary := StructuredSummary{
		Progress:      []string{"已完成词法分析器"},
		Uncertainties: []string{"编译失败：未定义符号"},
		UserActions:   []string{"下一步：补测试"},
	}
	got := structuredSummaryJSON(fixedMeta(), summary)
	s := string(got)
	for _, want := range []string{
		"已完成词法分析器",
		"编译失败：未定义符号",
		"下一步：补测试",
		"lexer 重构", // 元数据中的中文同样不被转义
	} {
		if !strings.Contains(s, want) {
			t.Errorf("CJK text %q was escaped or missing in:\n%s", want, s)
		}
	}
	if strings.Contains(s, `\u`) {
		t.Errorf("output contains \\u escapes, want raw UTF-8:\n%s", s)
	}
}

// TestStructuredSummaryJSONEmpty 验证空摘要返回空字节，且不因 nil meta 崩溃。
func TestStructuredSummaryJSONEmpty(t *testing.T) {
	cases := []struct {
		name    string
		meta    *Meta
		summary StructuredSummary
	}{
		{"zero summary with meta", fixedMeta(), StructuredSummary{}},
		{"only empty buckets", fixedMeta(), StructuredSummary{
			Progress:      []string{},
			Uncertainties: []string{},
			UserActions:   []string{},
		}},
		{"zero summary nil meta", nil, StructuredSummary{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := structuredSummaryJSON(tc.meta, tc.summary); len(got) != 0 {
				t.Fatalf("empty summary should return empty bytes, got %q", got)
			}
		})
	}
}

// TestStructuredSummaryJSONStableFieldOrder 验证字段顺序固定且与输入无关：
// 相同输入逐字节一致，不同输入时字段仍按同一顺序出现。
func TestStructuredSummaryJSONStableFieldOrder(t *testing.T) {
	want := `{"id":"session-01","title":"lexer 重构","updated":"2026-11-21T12:00:00Z","turns":7,"progress":["built lexer","added tests"],"uncertainties":["windows build fails"],"userActions":["review patch"]}`
	got := structuredSummaryJSON(fixedMeta(), fullStructuredSummary())
	if string(got) != want {
		t.Fatalf("unstable JSON output:\n got %s\nwant %s", got, want)
	}
	// 对同一输入重复序列化必须逐字节一致。
	for i := 0; i < 5; i++ {
		if again := structuredSummaryJSON(fixedMeta(), fullStructuredSummary()); !bytes.Equal(got, again) {
			t.Fatalf("output not stable across calls:\n%s\n%s", got, again)
		}
	}
	// 换一组元数据和摘要内容，字段顺序仍应保持一致。
	other := structuredSummaryJSON(&Meta{ID: "x"}, StructuredSummary{
		Progress: []string{"p"},
	})
	keys := []string{`"id"`, `"title"`, `"updated"`, `"turns"`, `"progress"`, `"uncertainties"`, `"userActions"`}
	last := -1
	for _, key := range keys {
		idx := bytes.Index(other, []byte(key))
		if idx < 0 {
			t.Fatalf("field %s missing in %s", key, other)
		}
		if idx <= last {
			t.Fatalf("field %s out of order in %s", key, other)
		}
		last = idx
	}
}

// TestStructuredSummaryJSONNilMeta 验证 nil meta 时以零值填充，不崩溃且字段齐全。
func TestStructuredSummaryJSONNilMeta(t *testing.T) {
	got := structuredSummaryJSON(nil, StructuredSummary{Progress: []string{"p"}})
	var d structuredSummaryJSONDTO
	if err := json.Unmarshal(got, &d); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if d.ID != "" || d.Title != "" || d.Turns != 0 {
		t.Errorf("nil meta should yield zero values, got %+v", d)
	}
	if !d.Updated.IsZero() {
		t.Errorf("nil meta Updated = %v, want zero time", d.Updated)
	}
}

// TestCompactionStructuredJSON 验证只读接线：可解析时输出 JSON，
// 空摘要或格式漂移时回退为 nil。
func TestCompactionStructuredJSON(t *testing.T) {
	parseable := Compaction{Summary: "## Progress\n- built lexer\n"}
	if got := parseable.StructuredJSON(fixedMeta()); len(got) == 0 {
		t.Fatal("StructuredJSON returned empty for a parseable compaction")
	} else if !bytes.Contains(got, []byte("built lexer")) {
		t.Errorf("JSON missing summary content: %s", got)
	}

	empty := Compaction{Summary: "## Progress\n- (none)\n"}
	if got := empty.StructuredJSON(fixedMeta()); len(got) != 0 {
		t.Fatalf("empty structured summary should yield nil, got %q", got)
	}

	drifted := Compaction{Summary: "just prose, no headings\n"}
	if got := drifted.StructuredJSON(fixedMeta()); len(got) != 0 {
		t.Fatalf("unparseable summary should yield nil, got %q", got)
	}
}
