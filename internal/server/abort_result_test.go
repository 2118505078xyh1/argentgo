package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestAbortResultSuccessAndReason 覆盖成功分支：cancelled 为 true，
// 普通 reason 原样保留，且返回值是可直接交给 writeJSON 的 map。
func TestAbortResultSuccessAndReason(t *testing.T) {
	got := abortResult(true, "用户主动停止")
	if got["cancelled"] != true {
		t.Fatalf("cancelled = %v, want true", got["cancelled"])
	}
	if got["reason"] != "用户主动停止" {
		t.Fatalf("reason = %v, want 用户主动停止", got["reason"])
	}
}

// TestAbortResultFailure 覆盖失败分支：cancelled 为 false，
// reason 仍被保留，便于调用方说明为何没有取消成功。
func TestAbortResultFailure(t *testing.T) {
	got := abortResult(false, "没有正在运行的轮次")
	if got["cancelled"] != false {
		t.Fatalf("cancelled = %v, want false", got["cancelled"])
	}
	if got["reason"] != "没有正在运行的轮次" {
		t.Fatalf("reason = %v", got["reason"])
	}
}

// TestAbortResultEmptyReason 固定空 reason 的行为：键仍存在且为空串，
// 既不会省略字段，也不会把 reason 变成 nil 或 <nil>。
func TestAbortResultEmptyReason(t *testing.T) {
	got := abortResult(true, "")
	reason, ok := got["reason"].(string)
	if !ok || reason != "" {
		t.Fatalf("reason = %#v, want non-nil empty string", got["reason"])
	}
}

// TestAbortResultRedactsReason 验证 reason 在生成时即被脱敏：
// 具有凭据形状的片段会被替换为占位符，且序列化后不会泄露原值。
func TestAbortResultRedactsReason(t *testing.T) {
	const secret = "sk-abcdef0123456789"
	got := abortResult(true, "已停止: api_key="+secret+" 谢谢")
	reason, _ := got["reason"].(string)
	if strings.Contains(reason, secret) {
		t.Fatalf("reason leaked secret: %q", reason)
	}
	if !strings.Contains(reason, "[redacted:") {
		t.Fatalf("reason lost redaction marker: %q", reason)
	}

	wire, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(wire), secret) {
		t.Fatalf("wire reply leaked secret: %s", wire)
	}
}

// TestAbortResultCJK 守护 CJK 安全性：非 ASCII 原因按 UTF-8 原样保留，
// 编码为 JSON 后仍是字面字符，而不是 \uXXXX 转义序列。
func TestAbortResultCJK(t *testing.T) {
	const reason = "用户取消了任务：正在收集中…（日本語もOK）"
	got := abortResult(true, reason)
	if got["reason"] != reason {
		t.Fatalf("reason = %v, want %q", got["reason"], reason)
	}
	wire, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(wire), reason) {
		t.Fatalf("CJK reason was mangled on the wire: %s", wire)
	}
	if strings.Contains(string(wire), `\u`) {
		t.Fatalf("CJK reason was escaped as \\uXXXX: %s", wire)
	}
}

// TestAbortDefaultResponseUnchanged 守护 opt-in 的向后兼容：未开启时
// /v1/abort 仍返回旧的 {"ok":true}，没有任何新增字段。
func TestAbortDefaultResponseUnchanged(t *testing.T) {
	runner := &fakeRunner{}
	h := NewHandler(Config{Token: "t", Runner: runner})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/abort", nil)
	req.Header.Set("Authorization", "Bearer t")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["ok"] != true || len(body) != 1 {
		t.Fatalf("default abort body = %v, want only {ok:true}", body)
	}
	if _, ok := body["cancelled"]; ok {
		t.Fatalf("default abort body gained a cancelled field: %v", body)
	}
}

// TestAbortStructuredResponseOptIn 验证显式开启后 /v1/abort 返回结构化
// 结果：runner 仍被取消，reason 取自 ?reason= 并经过脱敏，CJK 原样保留。
func TestAbortStructuredResponseOptIn(t *testing.T) {
	const secret = "sk-fedcba9876543210"
	reason := "用户取消：" + secret
	runner := &fakeRunner{}
	h := NewHandler(Config{Token: "t", Runner: runner, StructuredAbort: true})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/abort?reason="+url.QueryEscape(reason), nil)
	req.Header.Set("Authorization", "Bearer t")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !runner.abortedNow() {
		t.Fatal("runner was not aborted")
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["cancelled"] != true {
		t.Fatalf("cancelled = %v, want true", body["cancelled"])
	}
	gotReason, _ := body["reason"].(string)
	if strings.Contains(gotReason, secret) {
		t.Fatalf("structured abort leaked secret: %q", gotReason)
	}
	if !strings.Contains(gotReason, "用户取消") {
		t.Fatalf("structured abort lost CJK reason: %q", gotReason)
	}
	if _, ok := body["ok"]; ok {
		t.Fatalf("structured abort should not carry legacy ok field: %v", body)
	}
}
