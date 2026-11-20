package session

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestOmissionRecordPayload 覆盖常规情况：键名固定为 action/scope/target，
// 且不丢失任何 targetID，明确表达"省略某条记录及其结果"的语义。
func TestOmissionRecordPayload(t *testing.T) {
	got := omissionRecord("record-000123")
	want := map[string]string{
		"action": "omit",
		"scope":  "id_and_results",
		"target": "record-000123",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("payload = %v, want %v", got, want)
	}
}

// TestOmissionRecordCJK 确认中文 id 会原样编码进 JSONL，
// 不会被拆成 \uXXXX 转义序列，并且可以无损地解码回来。
func TestOmissionRecordCJK(t *testing.T) {
	payload := omissionRecord("失败的尝试-节点-甲")
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if !strings.Contains(string(data), "失败的尝试-节点-甲") {
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

// TestOmissionRecordEmpty 覆盖空 id 语义：返回非 nil 的空 map，
// 编码为 `{}` 而不是 `null`，且不生成任何省略动作键。
func TestOmissionRecordEmpty(t *testing.T) {
	got := omissionRecord("")
	if got == nil {
		t.Fatal("payload is nil, want non-nil empty map for empty targetID")
	}
	if len(got) != 0 {
		t.Fatalf("payload = %v, want empty map for empty targetID", got)
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if string(data) != "{}" {
		t.Fatalf("json = %s, want {}", data)
	}
}

// TestOmissionRecordStableKeys 确认相同输入总能得到相同字节，键按字典序排列，
// 并且返回的 map 彼此独立（修改一个不会影响另一个，保证纯函数语义）。
func TestOmissionRecordStableKeys(t *testing.T) {
	first := omissionRecord("target-a")
	second := omissionRecord("target-a")

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
	// encoding/json 对 map 键按字典序输出：action < scope < target。
	if want := `{"action":"omit","scope":"id_and_results","target":"target-a"}`; string(firstData) != want {
		t.Fatalf("json = %s, want %s", firstData, want)
	}

	first["target"] = "changed"
	if second["target"] != "target-a" {
		t.Fatalf("callers share the same map: second = %v", second)
	}
}
