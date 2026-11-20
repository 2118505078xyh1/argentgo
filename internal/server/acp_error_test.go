package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// TestFormatACPError 表驱动地覆盖 formatACPError 的全部分支：
// 认证失败、额度耗尽（含/不含充值链接）、普通错误原样返回、CJK 保留、
// URL 脱敏与 data 缺失。
func TestFormatACPError(t *testing.T) {
	tests := []struct {
		name    string
		code    int
		message string
		data    map[string]any
		want    string
	}{
		{
			name:    "auth required appends recovery hint",
			code:    codeServerError,
			message: "provider returned 401",
			data:    nil,
			want:    "provider returned 401" + acpHintSeparator + acpAuthRecoveryHint,
		},
		{
			name:    "auth required with empty message returns hint only",
			code:    codeServerError,
			message: "",
			data:    nil,
			want:    acpAuthRecoveryHint,
		},
		{
			name:    "credits exhausted with valid recharge url",
			code:    codeInternalError,
			message: "quota exceeded",
			data:    map[string]any{"reason": acpCreditsExhaustedReason, "url": "https://pay.example.com/topup"},
			want:    "quota exceeded" + acpHintSeparator + acpCreditsRecoveryHint + acpRechargeLinkPrefix + "https://pay.example.com/topup",
		},
		{
			name:    "credits exhausted without data still gives hint",
			code:    codeInternalError,
			message: "boom",
			data:    map[string]any{"reason": acpCreditsExhaustedReason},
			want:    "boom" + acpHintSeparator + acpCreditsRecoveryHint,
		},
		{
			name:    "normal error stays unchanged",
			code:    codeInternalError,
			message: "boom",
			data:    nil,
			want:    "boom",
		},
		{
			name:    "cjk message preserved unchanged",
			code:    codeInternalError,
			message: "额度不足，请稍后重试",
			data:    nil,
			want:    "额度不足，请稍后重试",
		},
		{
			name:    "cjk message with auth hint is not corrupted",
			code:    codeServerError,
			message: "登录已过期",
			data:    nil,
			want:    "登录已过期" + acpHintSeparator + acpAuthRecoveryHint,
		},
		{
			name:    "url userinfo is stripped",
			code:    codeInternalError,
			message: "quota",
			data:    map[string]any{"reason": acpCreditsExhaustedReason, "url": "https://user:secret@pay.example.com/topup?x=1"},
			want:    "quota" + acpHintSeparator + acpCreditsRecoveryHint + acpRechargeLinkPrefix + "https://pay.example.com/topup?x=1",
		},
		{
			name:    "non http url is dropped",
			code:    codeInternalError,
			message: "quota",
			data:    map[string]any{"reason": acpCreditsExhaustedReason, "url": "javascript:alert(1)"},
			want:    "quota" + acpHintSeparator + acpCreditsRecoveryHint,
		},
		{
			name:    "relative url is dropped",
			code:    codeInternalError,
			message: "quota",
			data:    map[string]any{"reason": acpCreditsExhaustedReason, "url": "/topup"},
			want:    "quota" + acpHintSeparator + acpCreditsRecoveryHint,
		},
		{
			name:    "unrelated reason leaves message unchanged",
			code:    codeInternalError,
			message: "boom",
			data:    map[string]any{"reason": "other"},
			want:    "boom",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatACPError(tc.code, tc.message, tc.data); got != tc.want {
				t.Fatalf("formatACPError(%d, %q, %v) = %q, want %q", tc.code, tc.message, tc.data, got, tc.want)
			}
		})
	}
}

// TestFormatACPErrorDoesNotLeakUserInfo 明确断言脱敏后的文案里
// 不再包含 userinfo 片段。
func TestFormatACPErrorDoesNotLeakUserInfo(t *testing.T) {
	got := formatACPError(codeInternalError, "quota", map[string]any{
		"reason": acpCreditsExhaustedReason,
		"url":    "https://alice:hunter2@pay.example.com/topup",
	})
	for _, leak := range []string{"alice", "hunter2", "alice:hunter2@"} {
		if strings.Contains(got, leak) {
			t.Fatalf("formatted message leaked %q: %s", leak, got)
		}
	}
	if !strings.Contains(got, "https://pay.example.com/topup") {
		t.Fatalf("formatted message lost the sanitized url: %s", got)
	}
}

// TestACPFailAppliesFriendlyHintOptIn 验证 acpState.fail 只在启用时
// 才调用 formatACPError，并且能把 turnEndErrorData 之类的 data 归一化。
func TestACPFailAppliesFriendlyHintOptIn(t *testing.T) {
	t.Run("enabled formats and sanitizes", func(t *testing.T) {
		var buf bytes.Buffer
		s := &acpState{peer: newPeer(&buf), friendlyErrors: true}
		s.fail(json.RawMessage("1"), codeServerError, "no runner configured", nil)
		body := decodeErrorEnvelope(t, buf.Bytes())
		if body["message"] != "no runner configured"+acpHintSeparator+acpAuthRecoveryHint {
			t.Fatalf("message = %v, want formatted auth hint", body["message"])
		}
	})

	t.Run("disabled keeps raw message", func(t *testing.T) {
		var buf bytes.Buffer
		s := &acpState{peer: newPeer(&buf)}
		s.fail(json.RawMessage("1"), codeServerError, "no runner configured", nil)
		body := decodeErrorEnvelope(t, buf.Bytes())
		if body["message"] != "no runner configured" {
			t.Fatalf("message = %v, want raw message", body["message"])
		}
	})

	t.Run("turn end data is compatible", func(t *testing.T) {
		var buf bytes.Buffer
		s := &acpState{peer: newPeer(&buf), friendlyErrors: true}
		s.fail(json.RawMessage("1"), codeInternalError, "boom", failureTurnEndData(TurnEndFailure))
		body := decodeErrorEnvelope(t, buf.Bytes())
		if body["message"] != "boom" {
			t.Fatalf("message = %v, want unchanged boom", body["message"])
		}
		data, _ := body["data"].(map[string]any)
		if data["turnEndReason"] != string(TurnEndFailure) {
			t.Fatalf("data = %v, want turnEndReason preserved", data)
		}
	})
}

// TestRunACPFriendlyErrorsIsOptIn 在传输层验证默认入口不改写消息，
// 而 RunACPWithFriendlyErrors 会给 -32000 错误追加恢复文案。
func TestRunACPFriendlyErrorsIsOptIn(t *testing.T) {
	for _, tc := range []struct {
		name       string
		run        func(context.Context, io.Reader, io.Writer, Config) error
		wantSuffix string
	}{
		{"default unchanged", RunACP, ""},
		{"opt in formatted", RunACPWithFriendlyErrors, acpHintSeparator + acpAuthRecoveryHint},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inR, inW := io.Pipe()
			outR, outW := io.Pipe()
			t.Cleanup(func() {
				_ = inW.Close()
				_ = outW.Close()
				_ = outR.Close()
			})
			done := make(chan error, 1)
			go func() {
				done <- tc.run(context.Background(), inR, outW, Config{})
			}()

			enc := json.NewEncoder(inW)
			dec := json.NewDecoder(outR)
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0", "id": 1, "method": "session/prompt",
				"params": map[string]any{"prompt": "hi"},
			})
			resp := readJSON(t, dec)
			errBody, _ := resp["error"].(map[string]any)
			if errBody["code"] != float64(codeServerError) {
				t.Fatalf("error code = %v, want %d (response %v)", errBody["code"], codeServerError, resp)
			}
			want := "no runner configured" + tc.wantSuffix
			if errBody["message"] != want {
				t.Fatalf("error message = %v, want %q", errBody["message"], want)
			}
			_ = inW.Close()
			waitFor(t, done)
		})
	}
}

// decodeErrorEnvelope 从一行 JSON-RPC 帧中取出 error 对象。
func decodeErrorEnvelope(t *testing.T, line []byte) map[string]any {
	t.Helper()
	var frame map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(line), &frame); err != nil {
		t.Fatalf("invalid json frame %q: %v", line, err)
	}
	body, ok := frame["error"].(map[string]any)
	if !ok {
		t.Fatalf("frame has no error object: %v", frame)
	}
	return body
}
