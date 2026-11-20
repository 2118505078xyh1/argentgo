package server

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"
)

// payloadKeys 返回 map 的键列表（已排序），用于比较字段集合是否稳定。
func payloadKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestSessionInfoUpdatePayloadStructure(t *testing.T) {
	payload := sessionInfoUpdate("标题", "sess_123", 1704067200)

	// 顶层是 ACP session/update 通知的 params，必须含会话 id 与 update。
	wantTop := []string{"sessionId", "update"}
	if got := payloadKeys(payload); !reflect.DeepEqual(got, wantTop) {
		t.Fatalf("顶层字段 = %v，期望 %v", got, wantTop)
	}
	if payload["sessionId"] != "sess_123" {
		t.Errorf("sessionId = %v，期望 %q", payload["sessionId"], "sess_123")
	}

	update, ok := payload["update"].(map[string]any)
	if !ok {
		t.Fatalf("update 类型 = %T，期望 map[string]any", payload["update"])
	}
	wantUpdate := []string{"meta", "sessionUpdate", "title", "updatedAt"}
	if got := payloadKeys(update); !reflect.DeepEqual(got, wantUpdate) {
		t.Fatalf("update 字段 = %v，期望 %v", got, wantUpdate)
	}
	if update["sessionUpdate"] != "session_info_update" {
		t.Errorf("sessionUpdate = %v，期望 %q", update["sessionUpdate"], acpSessionInfoUpdateKind)
	}
	if _, ok := update["meta"].(map[string]any); !ok {
		t.Errorf("meta 类型 = %T，期望 map[string]any", update["meta"])
	}

	// 整条载荷必须可序列化为合法 JSON-RPC 通知参数。
	if _, err := json.Marshal(payload); err != nil {
		t.Fatalf("载荷无法序列化：%v", err)
	}
}

func TestSessionInfoUpdateCJKTitle(t *testing.T) {
	// 覆盖汉字、日文假名与韩文，确认标题按字节安全传递且可 JSON 往返。
	title := "会话标题 — 日本語 / 한글"
	payload := sessionInfoUpdate(title, "sess_cjk", 1704067200)
	update := payload["update"].(map[string]any)
	if got := update["title"]; got != title {
		t.Fatalf("title = %q，期望 %q", got, title)
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("序列化失败：%v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("反序列化失败：%v", err)
	}
	got := decoded["update"].(map[string]any)["title"]
	if got != title {
		t.Fatalf("JSON 往返后 title = %q，期望 %q", got, title)
	}
}

func TestSessionInfoUpdateEmptyTitle(t *testing.T) {
	payload := sessionInfoUpdate("", "sess_empty", 0)
	update := payload["update"].(map[string]any)

	// 空标题也必须保留 title 键，调用方无需处理键缺失的分支。
	if got, ok := update["title"]; !ok || got != "" {
		t.Fatalf("空标题 title = (%v, %v)，期望 (\"\", true)", got, ok)
	}
	if got := payloadKeys(update); !reflect.DeepEqual(got, []string{"meta", "sessionUpdate", "title", "updatedAt"}) {
		t.Fatalf("空标题时字段集合 = %v，出现缺字段", got)
	}
}

func TestSessionInfoUpdateTimestamp(t *testing.T) {
	cases := []struct {
		name      string
		updatedAt int64
		want      string
	}{
		{"Unix 秒转为 RFC3339", 1704067200, "2024-01-01T00:00:00Z"},
		{"零时间戳为 Unix 纪元", 0, "1970-01-01T00:00:00Z"},
		{"负数时间戳", -1, "1969-12-31T23:59:59Z"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := sessionInfoUpdate("t", "s", tc.updatedAt)
			update := payload["update"].(map[string]any)
			if got := update["updatedAt"]; got != tc.want {
				t.Errorf("updatedAt = %v，期望 %q", got, tc.want)
			}
			// ACP 要求 updated_at 为字符串而不是裸数字。
			if _, ok := update["updatedAt"].(string); !ok {
				t.Errorf("updatedAt 类型 = %T，期望 string", update["updatedAt"])
			}
		})
	}
}

func TestSessionInfoUpdateStableFields(t *testing.T) {
	// 不同输入下字段集合必须完全一致，不存在 omitempty 式缺字段。
	inputs := []struct {
		title string
		id    string
		ts    int64
	}{
		{"", "", 0},
		{"普通标题", "sess_1", 1704067200},
		{"中文标题 😀", "sess_2", -123},
	}
	var wantTop, wantUpdate []string
	for i, in := range inputs {
		payload := sessionInfoUpdate(in.title, in.id, in.ts)
		top := payloadKeys(payload)
		update := payloadKeys(payload["update"].(map[string]any))
		if i == 0 {
			wantTop, wantUpdate = top, update
			continue
		}
		if !reflect.DeepEqual(top, wantTop) {
			t.Errorf("输入 %d 顶层字段 = %v，期望 %v", i, top, wantTop)
		}
		if !reflect.DeepEqual(update, wantUpdate) {
			t.Errorf("输入 %d update 字段 = %v，期望 %v", i, update, wantUpdate)
		}
	}
}
