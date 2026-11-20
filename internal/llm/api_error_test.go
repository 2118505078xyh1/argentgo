package llm_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"reflect"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

func TestClassifyAPIErrorFamilies(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
		step string // NextStep 中预期的子串
	}{
		{"not configured", errors.New("provider not configured: no api key"), llm.FamilyNotConfigured, "login"},
		{"auth 401", errors.New("provider returned 401 Unauthorized: invalid api key"), llm.FamilyAuth, "登录"},
		{"auth 403", errors.New("provider returned 403 Forbidden"), llm.FamilyAuth, "登录"},
		{"quota 402", errors.New("provider returned 402 Payment Required: insufficient quota"), llm.FamilyQuotaExhausted, "/model"},
		{"quota zh", errors.New("配额已用尽"), llm.FamilyQuotaExhausted, "/model"},
		{"rate limit 429", errors.New("provider returned 429 Too Many Requests: rate limit exceeded"), llm.FamilyRateLimit, "重试"},
		{"content filter", errors.New("provider returned 400 Bad Request: content_policy_violation"), llm.FamilyContentFilter, "提示词"},
		{"context overflow", errors.New("provider returned 400 Bad Request: prompt is too long"), llm.FamilyContextOverflow, "压缩"},
		{"model not found", errors.New("provider returned 404 Not Found: model gpt-foo not found"), llm.FamilyModelNotFound, "/model"},
		{"server 503", errors.New("provider returned 503 Service Unavailable"), llm.FamilyServer, "重试"},
		{"network string", errors.New("dial tcp 10.0.0.1:443: connection refused"), llm.FamilyNetwork, "网络"},
		{"network sentinel dns", fmt.Errorf("chat: %w", &net.DNSError{Err: "no such host", Name: "api.example.com"}), llm.FamilyNetwork, "网络"},
		{"network sentinel deadline", fmt.Errorf("read: %w", context.DeadlineExceeded), llm.FamilyNetwork, "网络"},
		{"network sentinel op error", fmt.Errorf("dial: %w", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}), llm.FamilyNetwork, "网络"},
		{"stream error sentinel", fmt.Errorf("%w: read failed", llm.ErrStreamError), llm.FamilyNetwork, "网络"},
		{"wrapped quota", fmt.Errorf("chat: %w", errors.New("provider returned 402 Payment Required: insufficient quota")), llm.FamilyQuotaExhausted, "/model"},
		{"wrapped model", fmt.Errorf("responses: request failed: %w", errors.New("provider returned 404 Not Found: unknown model")), llm.FamilyModelNotFound, "/model"},
		{"cancellation unknown", context.Canceled, llm.FamilyUnknown, "重试"},
		{"foreign error unknown", errors.New("some unrelated failure"), llm.FamilyUnknown, "重试"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := llm.ClassifyAPIError(tc.err)
			if got == nil {
				t.Fatal("ClassifyAPIError returned nil for a non-nil error")
			}
			if got.Family != tc.want {
				t.Errorf("family = %q, want %q (message %q)", got.Family, tc.want, got.Message)
			}
			if got.Message == "" {
				t.Error("Message must not be empty")
			}
			if !strings.Contains(got.NextStep, tc.step) {
				t.Errorf("NextStep %q must contain %q", got.NextStep, tc.step)
			}
		})
	}
}

// TestClassifyAPIErrorRateLimitNotShadowedByQuotaWord 覆盖这样的回复：
// 它提到配额/计费词，但实际描述的是短暂的限流。
// quotaRE 在 rateLimitRE 之前运行，因此裸的 "配额"/"额度"/"billing"
// 匹配会把它们归类为硬性配额（不可重试、路由到设置），并
// 阻止调用方重试。Retry.go 有意把相同措辞视为
// 可重试；ClassifyAPIError 必须保持一致。
func TestClassifyAPIErrorRateLimitNotShadowedByQuotaWord(t *testing.T) {
	rateLimits := []string{
		"请求过于频繁，配额将在30秒后重置",
		"请求过于频繁，请稍后重试（额度将在1分钟后恢复）",
		"rate limit exceeded, check your billing settings",
		"too many requests, quota will reset in 30 seconds",
	}
	for _, msg := range rateLimits {
		t.Run("rate limit: "+msg, func(t *testing.T) {
			err := errors.New(msg)
			if got := llm.ClassifyAPIError(err); got == nil || got.Family != llm.FamilyRateLimit {
				t.Errorf("ClassifyAPIError(%q) = %#v, want %q", msg, got, llm.FamilyRateLimit)
			}
			info := llm.ClassifyFailure(err)
			if info.Kind != llm.FailureRateLimit || !info.Retryable || info.UserAction != llm.UserActionRetry {
				t.Errorf("ClassifyFailure(%q) = %+v, want retryable rate_limit", msg, info)
			}
		})
	}

	// 显式的硬性限制仍必须胜出，即使它们带有
	// 相同的词；该修复不得把真正的配额耗尽
	// 变成可重试的限流。
	hardQuotas := []string{
		"配额已用尽",
		"额度不足，请充值",
		"insufficient quota, please upgrade",
		"provider returned 402 Payment Required: quota exceeded",
		"billing hard limit reached",
	}
	for _, msg := range hardQuotas {
		t.Run("hard quota: "+msg, func(t *testing.T) {
			if got := llm.ClassifyAPIError(errors.New(msg)); got == nil || got.Family != llm.FamilyQuotaExhausted {
				t.Errorf("ClassifyAPIError(%q) = %#v, want %q", msg, got, llm.FamilyQuotaExhausted)
			}
		})
	}
}

func TestClassifyAPIErrorNil(t *testing.T) {
	if got := llm.ClassifyAPIError(nil); got != nil {
		t.Fatalf("ClassifyAPIError(nil) = %#v, want nil", got)
	}
}

func TestClassifyAPIErrorRedactsCredentials(t *testing.T) {
	err := errors.New("provider returned 401 Unauthorized: api_key=sk-abcdefghijklmnop password=hunter2secret99 user@example.com")
	got := llm.ClassifyAPIError(err)
	if got == nil {
		t.Fatal("ClassifyAPIError returned nil")
	}
	if got.Family != llm.FamilyAuth {
		t.Errorf("family = %q, want %q", got.Family, llm.FamilyAuth)
	}
	blob := got.Message + " " + got.NextStep
	for _, secret := range []string{"sk-abcdefghijklmnop", "hunter2secret99", "user@example.com"} {
		if strings.Contains(blob, secret) {
			t.Errorf("output leaked %q: %q", secret, blob)
		}
	}
	if !strings.Contains(got.Message, "[redacted]") || !strings.Contains(got.Message, "[email]") {
		t.Errorf("expected redaction placeholders in %q", got.Message)
	}
}

func TestClassifyAPIErrorRedactsBeforeMatching(t *testing.T) {
	// 该密钥含有配额措辞；若分类在原始文本上运行，
	// 会错误地报告 quota_exhausted，而非 5xx 服务器失败。
	err := errors.New("provider returned 500 Internal Server Error: api_key=quota_exhausted_value_1234567890")
	got := llm.ClassifyAPIError(err)
	if got == nil || got.Family != llm.FamilyServer {
		t.Fatalf("family = %#v, want %q", got, llm.FamilyServer)
	}
	if strings.Contains(got.Message, "quota_exhausted_value") {
		t.Errorf("raw secret reached Message: %q", got.Message)
	}
}

func TestClassifyAPIErrorDeterministicAndIdempotent(t *testing.T) {
	err := fmt.Errorf("chat: %w", errors.New("provider returned 429 Too Many Requests: rate limit"))
	first := llm.ClassifyAPIError(err)
	for i := 0; i < 50; i++ {
		got := llm.ClassifyAPIError(err)
		if !reflect.DeepEqual(got, first) {
			t.Fatalf("call %d = %#v, want %#v", i, got, first)
		}
	}
	if again := llm.ClassifyAPIError(first); again != first {
		t.Errorf("reclassifying ActionableError returned %#v, want the same pointer", again)
	}
	if first.Error() != first.Message {
		t.Errorf("Error() = %q, want Message %q", first.Error(), first.Message)
	}
}

func TestClassifyAPIErrorBoundsMessage(t *testing.T) {
	err := errors.New("unknown failure: " + strings.Repeat("长", 500))
	got := llm.ClassifyAPIError(err)
	if got == nil {
		t.Fatal("ClassifyAPIError returned nil")
	}
	if runes := []rune(got.Message); len(runes) > 200 {
		t.Errorf("Message has %d runes, want <= 200", len(runes))
	}
}

func TestClassifyAPIErrorContextOverflowProviders(t *testing.T) {
	// 每条消息都是真实世界中表示输入/上下文超出模型窗口的提供方措辞。
	// 它们都必须映射到新家族，使调用方
	// 能通过压缩来响应，而不是被动失败。
	cases := []struct {
		name string
		err  error
	}{
		{"anthropic prompt too long", errors.New("prompt is too long: 250000 tokens > 200000 tokens")},
		{"anthropic context window exceeded", errors.New("context window exceeded")},
		{"openai maximum context length", errors.New("This model's maximum context length is 4097 tokens")},
		{"openai error code", errors.New("provider returned 400 Bad Request: context_length_exceeded")},
		{"openai tokens exceeds limit", errors.New("tokens exceeds the limit")},
		{"openai reduce messages", errors.New("Please reduce the length of the messages.")},
		{"exceeds the context window", errors.New("input exceeds the context window")},
		{"http 400 with keyword", errors.New("provider returned 400 Bad Request: prompt is too long")},
		{"zh context window", errors.New("上下文窗口已用尽")},
		{"zh prompt too long", errors.New("提示词过长")},
		{"zh input over context", errors.New("输入内容超出上下文长度")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := llm.ClassifyAPIError(tc.err)
			if got == nil {
				t.Fatal("ClassifyAPIError returned nil")
			}
			if got.Family != llm.FamilyContextOverflow {
				t.Errorf("family = %q, want %q (message %q)", got.Family, llm.FamilyContextOverflow, got.Message)
			}
			if !strings.Contains(got.NextStep, "压缩") {
				t.Errorf("NextStep %q should suggest compaction", got.NextStep)
			}
		})
	}
}

func TestClassifyAPIErrorContextOverflowDoesNotShadowOtherFamilies(t *testing.T) {
	// 添加新家族不得改变任何现有的分类。更强的
	// 信号保留其家族，通用的长度/限制措辞也不会
	// 被误认为上下文窗口溢出。
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"quota wins over overflow wording", errors.New("provider returned 402 Payment Required: insufficient quota, context window exceeded"), llm.FamilyQuotaExhausted},
		{"auth wins over overflow wording", errors.New("provider returned 401 Unauthorized: invalid api key, prompt is too long"), llm.FamilyAuth},
		{"rate limit", errors.New("provider returned 429 Too Many Requests: rate limit exceeded"), llm.FamilyRateLimit},
		{"bare quota", errors.New("quota exceeded"), llm.FamilyQuotaExhausted},
		{"context deadline is network", errors.New("context deadline exceeded"), llm.FamilyNetwork},
		{"content filter", errors.New("provider returned 400 Bad Request: content_policy_violation"), llm.FamilyContentFilter},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := llm.ClassifyAPIError(tc.err); got == nil || got.Family != tc.want {
				t.Errorf("family = %#v, want %q", got, tc.want)
			}
		})
	}

	// 输出 token 上限和载荷大小限制不是上下文窗口溢出：
	// 压缩无法解决它们，因此它们不得被该模式捕获。
	notOverflow := []error{
		errors.New("provider returned 400 Bad Request: maximum output tokens exceeded, reduce max_tokens"),
		errors.New("request payload is too large"),
		errors.New("response too long"),
		errors.New("some unrelated failure"),
	}
	for _, err := range notOverflow {
		if got := llm.ClassifyAPIError(err); got != nil && got.Family == llm.FamilyContextOverflow {
			t.Errorf("%v: wrongly classified as context overflow", err)
		}
	}
}

func TestClassifyAPIErrorContextOverflowRedactsAndBounds(t *testing.T) {
	// 恰好位于溢出措辞旁的凭据会在分类和显示之前
	// 被脱敏；CJK 消息保持字符安全。
	err := errors.New("prompt is too long: api_key=sk-abcdefghijklmnop context window exceeded user@example.com")
	got := llm.ClassifyAPIError(err)
	if got == nil || got.Family != llm.FamilyContextOverflow {
		t.Fatalf("family = %#v, want %q", got, llm.FamilyContextOverflow)
	}
	blob := got.Message + " " + got.NextStep
	for _, secret := range []string{"sk-abcdefghijklmnop", "user@example.com"} {
		if strings.Contains(blob, secret) {
			t.Errorf("output leaked %q: %q", secret, blob)
		}
	}

	long := errors.New("上下文窗口已用尽: " + strings.Repeat("长", 500))
	bounded := llm.ClassifyAPIError(long)
	if bounded == nil || bounded.Family != llm.FamilyContextOverflow {
		t.Fatalf("family = %#v, want %q", bounded, llm.FamilyContextOverflow)
	}
	if runes := []rune(bounded.Message); len(runes) > 200 {
		t.Errorf("Message has %d runes, want <= 200", len(runes))
	}
}
