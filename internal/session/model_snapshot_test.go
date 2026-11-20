package session

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestModelSnapshotRecordPayload 覆盖两个值都存在的常规情况：
// 键名固定为 provider/model，且不丢失任何输入。
func TestModelSnapshotRecordPayload(t *testing.T) {
	got := modelSnapshotRecord("anthropic", "claude-sonnet-4")
	want := map[string]string{"provider": "anthropic", "model": "claude-sonnet-4"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("payload = %v, want %v", got, want)
	}
}

// TestModelSnapshotRecordCJK 确认中文模型名与中文 provider 会原样编码进 JSONL，
// 不会被拆成 \uXXXX 转义序列，并且可以无损地解码回来。
func TestModelSnapshotRecordCJK(t *testing.T) {
	payload := modelSnapshotRecord("本地服务商", "通义千问-最大")
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if !strings.Contains(string(data), "本地服务商") || !strings.Contains(string(data), "通义千问-最大") {
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

// TestModelSnapshotRecordEmpty 覆盖空值语义：两个入参都为空时返回非 nil 的空 map，
// 编码为 `{}` 而不是 `null`；只有一个值为空时只保留非空的那一个。
func TestModelSnapshotRecordEmpty(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		model    string
		want     map[string]string
		wantJSON string
	}{
		{"两个都为空", "", "", map[string]string{}, "{}"},
		{"只有 provider", "openai", "", map[string]string{"provider": "openai"}, `{"provider":"openai"}`},
		{"只有 model", "", "gpt-5", map[string]string{"model": "gpt-5"}, `{"model":"gpt-5"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := modelSnapshotRecord(tc.provider, tc.model)
			if got == nil {
				t.Fatal("payload is nil, want non-nil empty map for empty inputs")
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

// TestModelSnapshotRecordStableKeys 确认相同输入总能得到相同字节，键按字典序排列，
// 并且返回的 map 彼此独立（修改一个不会影响另一个，保证纯函数语义）。
func TestModelSnapshotRecordStableKeys(t *testing.T) {
	first := modelSnapshotRecord("provider-a", "model-b")
	second := modelSnapshotRecord("provider-a", "model-b")

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
	// encoding/json 对 map 键按字典序输出，因此 "model" 排在 "provider" 前面。
	if want := `{"model":"model-b","provider":"provider-a"}`; string(firstData) != want {
		t.Fatalf("json = %s, want %s", firstData, want)
	}

	first["model"] = "changed"
	if second["model"] != "model-b" {
		t.Fatalf("callers share the same map: second = %v", second)
	}
}

// TestHeaderModelFieldsOmittedWhenEmpty 确认未设置 Provider/Model 的 Header
// 编码字节与旧版本完全一致，不会破坏已有会话的文件格式。
func TestHeaderModelFieldsOmittedWhenEmpty(t *testing.T) {
	header := Header{Title: "旧会话", Created: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	data, err := json.Marshal(header)
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	const want = `{"title":"旧会话","created":"2026-01-01T00:00:00Z"}`
	if string(data) != want {
		t.Fatalf("header json = %s, want %s", data, want)
	}
}

// TestHeaderLegacyJSONCompatible 确认缺少新字段的旧 JSON 能照常解码，
// 同时带新字段的 JSON 也能无损往返，供 resume 读取模型快照。
func TestHeaderLegacyJSONCompatible(t *testing.T) {
	var legacy Header
	if err := json.Unmarshal([]byte(`{"title":"legacy","created":"2026-01-01T00:00:00Z"}`), &legacy); err != nil {
		t.Fatalf("unmarshal legacy header: %v", err)
	}
	if legacy.Title != "legacy" || legacy.Provider != "" || legacy.Model != "" {
		t.Fatalf("legacy header = %+v", legacy)
	}

	want := Header{
		Title:    "new",
		Created:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Provider: "本地服务商",
		Model:    "模型-α",
	}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	var got Header
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal header: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
}
