package session

import (
	"encoding/json"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// TestForkLineagePayload 覆盖常规情况：键名固定为 parent_id/at_record/ts，
// 且不丢失任何输入，ts 以十进制字符串保存。
func TestForkLineagePayload(t *testing.T) {
	got := forkLineagePayload("parent-session", "n000007", 1763712345)
	want := map[string]string{
		"parent_id": "parent-session",
		"at_record": "n000007",
		"ts":        "1763712345",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("payload = %v, want %v", got, want)
	}
}

// TestForkLineageCJK 确认中文会话 id 与记录 id 会原样编码进 JSONL，
// 不会被拆成 \uXXXX 转义序列，并且可以无损地解码回来。
func TestForkLineageCJK(t *testing.T) {
	payload := forkLineagePayload("测试用户", "记录-甲", 1763712345)
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if !strings.Contains(string(data), "测试用户") || !strings.Contains(string(data), "记录-甲") {
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

// TestForkLineageEmpty 覆盖空值语义：三个值都为空时返回非 nil 的空 map，
// 编码为 `{}` 而不是 `null`；只有部分值为空时只保留非空的字段。
func TestForkLineageEmpty(t *testing.T) {
	tests := []struct {
		name     string
		parentID string
		atRecord string
		ts       int64
		want     map[string]string
		wantJSON string
	}{
		{"全部为空", "", "", 0, map[string]string{}, "{}"},
		{"只有 parent_id", "parent", "", 0, map[string]string{"parent_id": "parent"}, `{"parent_id":"parent"}`},
		{"只有 at_record", "", "n000001", 0, map[string]string{"at_record": "n000001"}, `{"at_record":"n000001"}`},
		{"只有 ts", "", "", 1763712345, map[string]string{"ts": "1763712345"}, `{"ts":"1763712345"}`},
		{"parent_id 与 at_record", "parent", "n000001", 0,
			map[string]string{"at_record": "n000001", "parent_id": "parent"},
			`{"at_record":"n000001","parent_id":"parent"}`},
		{"全部字段", "parent", "n000001", 1763712345,
			map[string]string{"at_record": "n000001", "parent_id": "parent", "ts": "1763712345"},
			`{"at_record":"n000001","parent_id":"parent","ts":"1763712345"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := forkLineagePayload(tc.parentID, tc.atRecord, tc.ts)
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

// TestForkLineageStableKeys 确认相同输入总能得到相同字节，键按字典序排列，
// 并且返回的 map 彼此独立（修改一个不会影响另一个，保证纯函数语义）。
func TestForkLineageStableKeys(t *testing.T) {
	first := forkLineagePayload("parent", "n000001", 1763712345)
	second := forkLineagePayload("parent", "n000001", 1763712345)

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
	// encoding/json 对 map 键按字典序输出：at_record < parent_id < ts。
	if want := `{"at_record":"n000001","parent_id":"parent","ts":"1763712345"}`; string(firstData) != want {
		t.Fatalf("json = %s, want %s", firstData, want)
	}

	first["parent_id"] = "changed"
	if second["parent_id"] != "parent" {
		t.Fatalf("callers share the same map: second = %v", second)
	}
}

// TestForkLineageTimestampFormatting 覆盖时间戳边界：最大值、最小值、负数、
// 以及 0（视为空值），确认十进制格式化不会溢出或丢失精度。
func TestForkLineageTimestampFormatting(t *testing.T) {
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
			got := forkLineagePayload("parent", "n000001", tc.ts)
			if got["ts"] != tc.want {
				t.Fatalf("ts = %q, want %q", got["ts"], tc.want)
			}
		})
	}
}

// TestForkLineagePreservesWhitespaceValues 确认空值判定只针对精确空字符串：
// 纯空白值按普通值保留，不会被误当作空值丢弃。
func TestForkLineagePreservesWhitespaceValues(t *testing.T) {
	got := forkLineagePayload("  ", "\t", 0)
	want := map[string]string{"parent_id": "  ", "at_record": "\t"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("payload = %v, want %v", got, want)
	}
}
