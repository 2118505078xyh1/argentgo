package session

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// TestNewCondensationEventDedupSort 验证 forgottenIDs 去重且按字典序排序，
// 并保留摘要与偏移量。
func TestNewCondensationEventDedupSort(t *testing.T) {
	e := NewCondensationEvent([]string{"e3", "e1", "e2", "e1", "e3", "e2"}, "摘要", 7)
	if want := []string{"e1", "e2", "e3"}; !reflect.DeepEqual(e.ForgottenIDs, want) {
		t.Fatalf("ForgottenIDs = %q, want %q", e.ForgottenIDs, want)
	}
	if e.Summary != "摘要" || e.Offset != 7 {
		t.Fatalf("metadata mismatch: %+v", e)
	}
}

// TestNewCondensationEventDoesNotMutateInput 验证构造过程不修改传入的切片。
func TestNewCondensationEventDoesNotMutateInput(t *testing.T) {
	in := []string{"z", "a", "z"}
	orig := append([]string(nil), in...)
	_ = NewCondensationEvent(in, "s", 0)
	if !reflect.DeepEqual(in, orig) {
		t.Fatalf("input slice was mutated: got %q, want %q", in, orig)
	}
}

// TestCondensationEventMarshalCJK 验证中文字段原样输出、不转义为 \uXXXX，
// 且字段名与顺序稳定。
func TestCondensationEventMarshalCJK(t *testing.T) {
	e := NewCondensationEvent([]string{"e1"}, "已完成词法分析器重构", 3)
	got := string(e.Marshal())
	if !strings.Contains(got, "已完成词法分析器重构") {
		t.Errorf("CJK summary was escaped or missing: %s", got)
	}
	if strings.Contains(got, `\u`) {
		t.Errorf("output contains \\u escapes, want raw UTF-8: %s", got)
	}
	for _, key := range []string{`"forgotten_event_ids"`, `"summary"`, `"summary_offset"`} {
		if !strings.Contains(got, key) {
			t.Errorf("missing field %s in %s", key, got)
		}
	}
	// 再次序列化应逐字节一致（可复现）。
	if again := string(e.Marshal()); again != got {
		t.Errorf("Marshal not deterministic:\n%s\n%s", got, again)
	}
}

// TestCondensationEventRoundTrip 验证 Marshal/Parse 往返无损，含空事件。
func TestCondensationEventRoundTrip(t *testing.T) {
	cases := []CondensationEvent{
		NewCondensationEvent([]string{"b", "a", "a"}, "中文摘要", 12),
		NewCondensationEvent(nil, "", 0),
		NewCondensationEvent([]string{}, "plain ascii summary", -1),
	}
	for _, want := range cases {
		data := want.Marshal()
		got, err := ParseCondensationEvent(data)
		if err != nil {
			t.Fatalf("ParseCondensationEvent(%s) failed: %v", data, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", got, want)
		}
	}
}

// TestParseCondensationEventEmptyArray 验证空 forgotten_event_ids 还原为空数组而非 nil。
func TestParseCondensationEventEmptyArray(t *testing.T) {
	got, err := ParseCondensationEvent([]byte(`{"forgotten_event_ids":[],"summary":"","summary_offset":0}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ForgottenIDs == nil || len(got.ForgottenIDs) != 0 {
		t.Fatalf("ForgottenIDs = %#v, want non-nil empty slice", got.ForgottenIDs)
	}
}

// TestParseCondensationEventNormalizesIDs 验证解析后同样去重排序，
// 即便输入 JSON 中的 ID 乱序且重复。
func TestParseCondensationEventNormalizesIDs(t *testing.T) {
	got, err := ParseCondensationEvent([]byte(`{"forgotten_event_ids":["c","a","b","a"],"summary":"s","summary_offset":5}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := []string{"a", "b", "c"}; !reflect.DeepEqual(got.ForgottenIDs, want) {
		t.Fatalf("ForgottenIDs = %q, want %q", got.ForgottenIDs, want)
	}
}

// TestParseCondensationEventInvalid 验证空/非法输入返回错误，
// 且 JSON 语法错误可用 errors.Is 追溯到 encoding/json 的原始错误。
func TestParseCondensationEventInvalid(t *testing.T) {
	cases := [][]byte{
		nil,
		{},
		[]byte("   \n\t"),
		[]byte("{bad json"),
		[]byte(`{"summary":`),
		[]byte("null"),
	}
	for _, data := range cases {
		if _, err := ParseCondensationEvent(data); err == nil {
			t.Errorf("expected error for input %q", data)
		}
	}

	var syntaxErr *json.SyntaxError
	_, err := ParseCondensationEvent([]byte("{bad json"))
	if !errors.As(err, &syntaxErr) {
		t.Fatalf("expected wrapped json.SyntaxError, got %v", err)
	}
}

// TestParseCondensationEventDoesNotMutateInput 验证解析不修改传入的字节切片。
func TestParseCondensationEventDoesNotMutateInput(t *testing.T) {
	in := []byte(`{"forgotten_event_ids":["b","a"],"summary":"中文","summary_offset":1}`)
	orig := append([]byte(nil), in...)
	if _, err := ParseCondensationEvent(in); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(in, orig) {
		t.Fatalf("input bytes were mutated: got %s, want %s", in, orig)
	}
}
