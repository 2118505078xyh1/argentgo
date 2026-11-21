package session

import (
	"encoding/json"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// TestRevisionNotePayload 覆盖常规情况：键名固定为 kind/actor/ts，
// 且不丢失任何输入，ts 以十进制字符串保存。
func TestRevisionNotePayload(t *testing.T) {
	got := revisionNote("compact", "agent", 1763712345)
	want := map[string]string{
		"kind":  "compact",
		"actor": "agent",
		"ts":    "1763712345",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("payload = %v, want %v", got, want)
	}
}

// TestRevisionNoteCJK 确认中文执行者与中文修订类型会原样编码进 JSONL，
// 不会被拆成 \uXXXX 转义序列，并且可以无损地解码回来。
func TestRevisionNoteCJK(t *testing.T) {
	payload := revisionNote("压缩", "寒弦问月夜未阑", 1763712345)
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if !strings.Contains(string(data), "压缩") || !strings.Contains(string(data), "寒弦问月夜未阑") {
		t.Fatalf("CJK text was escaped or lost: %s", data)
	}
	if strings.Contains(string(data), `\u`) {
		t.Fatalf("CJK text must not be escaped as \\uXXXX: %s", data)
	}
	var decoded map[string]string
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if !reflect.DeepEqual(decoded, payload) {
		t.Fatalf("round trip = %v, want %v", decoded, payload)
	}
}

// TestRevisionNoteEmpty 覆盖空值语义：三个值都为空时返回非 nil 的空 map，
// 编码为 `{}` 而不是 `null`；只有部分值为空时只保留非空的字段。
func TestRevisionNoteEmpty(t *testing.T) {
	tests := []struct {
		name     string
		kind     string
		actor    string
		ts       int64
		want     map[string]string
		wantJSON string
	}{
		{"全部为空", "", "", 0, map[string]string{}, "{}"},
		{"只有 kind", "compact", "", 0, map[string]string{"kind": "compact"}, `{"kind":"compact"}`},
		{"只有 actor", "", "agent", 0, map[string]string{"actor": "agent"}, `{"actor":"agent"}`},
		{"只有 ts", "", "", 1763712345, map[string]string{"ts": "1763712345"}, `{"ts":"1763712345"}`},
		{"kind 与 actor", "compact", "agent", 0,
			map[string]string{"actor": "agent", "kind": "compact"},
			`{"actor":"agent","kind":"compact"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := revisionNote(tc.kind, tc.actor, tc.ts)
			if got == nil {
				t.Fatal("payload is nil, want non-nil map")
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("payload = %v, want %v", got, tc.want)
			}
			data, err := json.Marshal(got)
			if err != nil {
				t.Fatalf("marshal payload: %v", err)
			}
			if string(data) != tc.wantJSON {
				t.Fatalf("json = %s, want %s", data, tc.wantJSON)
			}
		})
	}
}

// TestRevisionNoteStableKeys 确认相同输入总能得到相同字节，键按字典序排列，
// 并且返回的 map 彼此独立（修改一个不会影响另一个，保证纯函数语义）。
func TestRevisionNoteStableKeys(t *testing.T) {
	first := revisionNote("compact", "agent", 1763712345)
	second := revisionNote("compact", "agent", 1763712345)

	firstData, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal first payload: %v", err)
	}
	secondData, err := json.Marshal(second)
	if err != nil {
		t.Fatalf("marshal second payload: %v", err)
	}
	if string(firstData) != string(secondData) {
		t.Fatalf("unstable encoding: %s vs %s", firstData, secondData)
	}
	// encoding/json 对 map 键按字典序输出：actor < kind < ts。
	if want := `{"actor":"agent","kind":"compact","ts":"1763712345"}`; string(firstData) != want {
		t.Fatalf("json = %s, want %s", firstData, want)
	}

	first["actor"] = "changed"
	if second["actor"] != "agent" {
		t.Fatalf("callers share the same map: second = %v", second)
	}
}

// TestRevisionNoteTimestampFormatting 覆盖时间戳边界：最大值、最小值、负数、
// 以及 0（视为空值），确认十进制格式化不会溢出或丢失精度。
func TestRevisionNoteTimestampFormatting(t *testing.T) {
	tests := []struct {
		name string
		ts   int64
		want string
	}{
		{"最大值", math.MaxInt64, strconv.FormatInt(math.MaxInt64, 10)},
		{"最小值", math.MinInt64, strconv.FormatInt(math.MinInt64, 10)},
		{"负数", -1, "-1"},
		{"正数", 42, "42"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := revisionNote("compact", "agent", tc.ts)
			if got["ts"] != tc.want {
				t.Fatalf("ts = %q, want %q", got["ts"], tc.want)
			}
		})
	}
}
