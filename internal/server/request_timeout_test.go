package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestRequestTimeoutFromHeader 表驱动覆盖解析规则：合法值、空白、
// 非法（含 CJK）、非正数、超大钳制与默认回退。
func TestRequestTimeoutFromHeader(t *testing.T) {
	const fallback = 30000
	for _, tc := range []struct {
		name      string
		header    string
		defaultMS int
		want      int
	}{
		{"合法值", "1500", fallback, 1500},
		{"前后空白", "  2500  ", fallback, 2500},
		{"恰好等于上限", strconv.Itoa(maxRequestTimeoutMS), fallback, maxRequestTimeoutMS},
		{"超过上限被钳制", strconv.Itoa(maxRequestTimeoutMS + 1), fallback, maxRequestTimeoutMS},
		{"远超上限被钳制", "999999999", fallback, maxRequestTimeoutMS},
		{"正整数范围溢出被钳制", "999999999999999999999999999", fallback, maxRequestTimeoutMS},
		{"带正号", "+800", fallback, 800},
		{"负值回退", "-5", fallback, fallback},
		{"负整数范围溢出回退", "-999999999999999999999999999", fallback, fallback},
		{"零回退", "0", fallback, fallback},
		{"负零回退", "-0", fallback, fallback},
		{"空回退", "", fallback, fallback},
		{"仅空白回退", "   \t ", fallback, fallback},
		{"非数字回退", "abc", fallback, fallback},
		{"小数回退", "1000.5", fallback, fallback},
		{"单位后缀回退", "1s", fallback, fallback},
		{"CJK 回退", "超时", fallback, fallback},
		{"CJK 后缀回退", "1000毫秒", fallback, fallback},
		{"全角数字回退", "１０００", fallback, fallback},
		{"自定义默认回退", "", 1234, 1234},
		{"默认值原样回退", "不合法", 999999999, 999999999},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := requestTimeoutFromHeader(tc.header, tc.defaultMS); got != tc.want {
				t.Fatalf("requestTimeoutFromHeader(%q, %d) = %d, want %d", tc.header, tc.defaultMS, got, tc.want)
			}
		})
	}
}

// TestRequestTimeoutHeaderName 固定请求头名，避免接线方拼错大小写或前缀。
func TestRequestTimeoutHeaderName(t *testing.T) {
	if requestTimeoutHeaderName != "X-Request-Timeout" {
		t.Fatalf("requestTimeoutHeaderName = %q, want X-Request-Timeout", requestTimeoutHeaderName)
	}
}

// requestTimeoutProbeRunner 记录 Runner.Prompt 收到的 context 是否带有截止时间。
type requestTimeoutProbeRunner struct {
	hasDeadline bool
	deadline    time.Time
}

func (r *requestTimeoutProbeRunner) Prompt(ctx context.Context, _ string, _ func(string)) (string, error) {
	r.deadline, r.hasDeadline = ctx.Deadline()
	return "ok", nil
}

func (r *requestTimeoutProbeRunner) Abort() {}

// promptWithHeader 发送一次 /v1/prompt，可指定配置与 X-Request-Timeout 头。
func promptWithHeader(t *testing.T, cfg Config, timeoutHeader string) (*httptest.ResponseRecorder, *requestTimeoutProbeRunner) {
	t.Helper()
	probe := &requestTimeoutProbeRunner{}
	cfg.Token = "t"
	cfg.Runner = probe
	h := NewHandler(cfg)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/prompt", strings.NewReader(`{"text":"hi"}`))
	req.Header.Set("Authorization", "Bearer t")
	if timeoutHeader != "" {
		req.Header.Set(requestTimeoutHeaderName, timeoutHeader)
	}
	h.ServeHTTP(rec, req)
	return rec, probe
}

// TestRequestTimeoutWiringIsOptIn 验证默认关闭时不设置截止时间，
// 显式开启后才按请求头设置；非法头回退到默认值。
func TestRequestTimeoutWiringIsOptIn(t *testing.T) {
	t.Run("默认关闭不设置截止时间", func(t *testing.T) {
		_, probe := promptWithHeader(t, Config{}, "1500")
		if probe.hasDeadline {
			t.Fatal("禁用超时时不应设置截止时间")
		}
	})
	t.Run("开启后按请求头设置截止时间", func(t *testing.T) {
		_, probe := promptWithHeader(t, Config{EnableRequestTimeout: true}, "1500")
		if !probe.hasDeadline {
			t.Fatal("启用超时时应设置截止时间")
		}
		if d := time.Until(probe.deadline); d <= 0 || d > 1500*time.Millisecond {
			t.Fatalf("截止时间剩余 %v，应落在 (0, 1.5s]", d)
		}
	})
	t.Run("开启后缺省头使用默认超时", func(t *testing.T) {
		_, probe := promptWithHeader(t, Config{EnableRequestTimeout: true, DefaultRequestTimeoutMS: 4000}, "")
		if !probe.hasDeadline {
			t.Fatal("启用超时时应设置截止时间")
		}
		if d := time.Until(probe.deadline); d <= 0 || d > 4000*time.Millisecond {
			t.Fatalf("截止时间剩余 %v，应落在 (0, 4s]", d)
		}
	})
	t.Run("开启后非法头回退默认超时", func(t *testing.T) {
		_, probe := promptWithHeader(t, Config{EnableRequestTimeout: true, DefaultRequestTimeoutMS: 4000}, "超时")
		if !probe.hasDeadline {
			t.Fatal("启用超时时应设置截止时间")
		}
		if d := time.Until(probe.deadline); d <= 0 || d > 4000*time.Millisecond {
			t.Fatalf("截止时间剩余 %v，应落在 (0, 4s]", d)
		}
	})
}

// TestRequestTimeoutFailureClassifiedAsFailure 守护错误分类：
// 超时不是用户取消，流前失败仍以 500 和 context deadline exceeded 报告。
func TestRequestTimeoutFailureClassifiedAsFailure(t *testing.T) {
	failing := &failingRunner{err: context.DeadlineExceeded}
	cfg := Config{EnableRequestTimeout: true, DefaultRequestTimeoutMS: 1500}
	cfg.Token = "t"
	cfg.Runner = failing
	h := NewHandler(cfg)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/prompt", strings.NewReader(`{"text":"hi"}`))
	req.Header.Set("Authorization", "Bearer t")
	req.Header.Set(requestTimeoutHeaderName, "1500")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body %q)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), context.DeadlineExceeded.Error()) {
		t.Fatalf("body = %q, want deadline error text", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "cancelled") {
		t.Fatalf("超时不应被归类为用户取消: %q", rec.Body.String())
	}
}
