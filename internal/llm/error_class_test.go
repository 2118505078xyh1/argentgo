package llm_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

func TestClassifyFailureKinds(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want llm.FailureInfo
	}{
		{"nil", nil, llm.FailureInfo{llm.FailureUnknown, false, llm.UserActionNone, "unknown"}},
		{"not configured", errors.New("provider not configured: no api key"), llm.FailureInfo{llm.FailureConfig, false, llm.UserActionSettings, "config.not_configured"}},
		{"auth 401", errors.New("provider returned 401 Unauthorized: invalid api key"), llm.FailureInfo{llm.FailureAuth, false, llm.UserActionSettings, "auth.invalid_key"}},
		{"auth 403", errors.New("provider returned 403 Forbidden"), llm.FailureInfo{llm.FailureAuth, false, llm.UserActionSettings, "auth.invalid_key"}},
		{"quota 402", errors.New("provider returned 402 Payment Required: insufficient quota"), llm.FailureInfo{llm.FailureQuota, false, llm.UserActionSettings, "quota.exhausted"}},
		{"rate limit 429", errors.New("provider returned 429 Too Many Requests: rate limit exceeded"), llm.FailureInfo{llm.FailureRateLimit, true, llm.UserActionRetry, "rate_limit.exceeded"}},
		{"content filter", errors.New("provider returned 400 Bad Request: content_policy_violation"), llm.FailureInfo{llm.FailureAgentAction, false, llm.UserActionRetry, "agent_action.content_filter"}},
		{"context overflow", errors.New("provider returned 400 Bad Request: prompt is too long"), llm.FailureInfo{llm.FailureContextOverflow, false, llm.UserActionSettings, "context_overflow.exceeded"}},
		{"model not found", errors.New("provider returned 404 Not Found: model gpt-foo not found"), llm.FailureInfo{llm.FailureConfig, false, llm.UserActionSettings, "config.model_not_found"}},
		{"server 503", errors.New("provider returned 503 Service Unavailable"), llm.FailureInfo{llm.FailureTransient, true, llm.UserActionRetry, "transient.server"}},
		{"network string", errors.New("dial tcp 10.0.0.1:443: connection refused"), llm.FailureInfo{llm.FailureTransient, true, llm.UserActionRetry, "transient.network"}},
		{"wrapped network sentinel", fmt.Errorf("chat: %w", &net.DNSError{Err: "no such host", Name: "api.example.com"}), llm.FailureInfo{llm.FailureTransient, true, llm.UserActionRetry, "transient.network"}},
		{"deadline sentinel", fmt.Errorf("read: %w", context.DeadlineExceeded), llm.FailureInfo{llm.FailureTransient, true, llm.UserActionRetry, "transient.network"}},
		{"stream sentinel", fmt.Errorf("%w: read failed", llm.ErrStreamError), llm.FailureInfo{llm.FailureTransient, true, llm.UserActionRetry, "transient.network"}},
		{"cancellation", context.Canceled, llm.FailureInfo{llm.FailureUnknown, false, llm.UserActionNone, "unknown"}},
		{"foreign error", errors.New("some unrelated failure"), llm.FailureInfo{llm.FailureUnknown, false, llm.UserActionNone, "unknown"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := llm.ClassifyFailure(tc.err)
			if got != tc.want {
				t.Errorf("ClassifyFailure(%v) = %+v, want %+v", tc.err, got, tc.want)
			}
		})
	}
}

func TestClassifyFailureIdempotentOnActionableError(t *testing.T) {
	// ClassifyAPIError 对其自身类型是幂等的；ClassifyFailure 继承
	// 这一点，因为它转换的是已规范化的家族。
	err := llm.ClassifyAPIError(errors.New("provider returned 401 Unauthorized"))
	first := llm.ClassifyFailure(err)
	for i := 0; i < 10; i++ {
		if got := llm.ClassifyFailure(err); got != first {
			t.Fatalf("call %d = %+v, want %+v", i, got, first)
		}
	}
	if got := llm.ClassifyFailure(llm.ClassifyAPIError(err)); got != first {
		t.Errorf("classifying an ActionableError again = %+v, want %+v", got, first)
	}
}

func TestClassifyFailureAuthoritativeExceptionBeforeText(t *testing.T) {
	// 文本提到了配额/401，但哨兵是权威的：DNS 或
	// 截止时间失败必须保持 transient。这验证了复用
	// ClassifyAPIError 的顺序，而非第二个纯文本分类器。
	cases := []struct {
		name string
		err  error
	}{
		{"dns with quota wording", fmt.Errorf("chat: %w", &net.DNSError{Err: "quota exhausted", Name: "api.example.com"})},
		{"deadline with 401 wording", fmt.Errorf("401 unauthorized: %w", context.DeadlineExceeded)},
		{"stream with 402 wording", fmt.Errorf("%w: 402 payment required", llm.ErrStreamError)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := llm.ClassifyFailure(tc.err)
			if got.Kind != llm.FailureTransient || got.ErrorID == "" {
				t.Errorf("kind = %q, id = %q; want transient sentinel result, got %+v", got.Kind, got.ErrorID, got)
			}
			if got.ErrorID == "quota.exhausted" || got.ErrorID == "auth.invalid_key" {
				t.Errorf("text pattern overrode the sentinel: %+v", got)
			}
		})
	}
}

func TestClassifyFailureRetryableMatchesClassifyRetryError(t *testing.T) {
	// 对于传输层失败，ClassifyRetryError 的 ReasonNone 是唯一
	// 的"不重试"答案；Retryable 必须与它一致。
	cases := []struct {
		name string
		err  error
	}{
		{"nil", nil},
		{"cancellation", context.Canceled},
		{"dns", &net.DNSError{Err: "no such host", Name: "api.example.com"}},
		{"deadline", context.DeadlineExceeded},
		{"op error", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}},
		{"stream", llm.ErrStreamError},
		{"eof", io.EOF},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := llm.ClassifyRetryError(tc.err) != llm.ReasonNone
			if got := llm.ClassifyFailure(tc.err).Retryable; got != want {
				t.Errorf("Retryable = %v, want %v (ClassifyRetryError = %q)", got, want, llm.ClassifyRetryError(tc.err))
			}
		})
	}
}

func TestClassifyFailureUserActionRouting(t *testing.T) {
	// settings：重试前用户必须修复凭据/模型/计费。
	settings := []error{
		errors.New("provider not configured: no api key"),
		errors.New("provider returned 401 Unauthorized: invalid api key"),
		errors.New("provider returned 402 Payment Required: insufficient quota"),
		errors.New("provider returned 404 Not Found: unknown model"),
	}
	for _, err := range settings {
		if got := llm.ClassifyFailure(err).UserAction; got != llm.UserActionSettings {
			t.Errorf("%v: UserAction = %q, want %q", err, got, llm.UserActionSettings)
		}
	}

	// retry：等待或修改请求就足够了。
	retry := []error{
		errors.New("provider returned 429 Too Many Requests: rate limit exceeded"),
		errors.New("provider returned 503 Service Unavailable"),
		errors.New("dial tcp 10.0.0.1:443: connection refused"),
		errors.New("provider returned 400 Bad Request: content_policy_violation"),
	}
	for _, err := range retry {
		if got := llm.ClassifyFailure(err).UserAction; got != llm.UserActionRetry {
			t.Errorf("%v: UserAction = %q, want %q", err, got, llm.UserActionRetry)
		}
	}

	// none：用户无可采取的动作。
	none := []error{
		nil,
		context.Canceled,
		errors.New("some unrelated failure"),
	}
	for _, err := range none {
		if got := llm.ClassifyFailure(err).UserAction; got != llm.UserActionNone {
			t.Errorf("%v: UserAction = %q, want %q", err, got, llm.UserActionNone)
		}
	}
}

func TestClassifyFailureErrorIDStableAndRedacted(t *testing.T) {
	safeID := regexp.MustCompile(`^(?:unknown|[a-z_]+(?:\.[a-z_]+)?)$`)

	cases := []struct {
		name   string
		first  error
		second error
		id     string
		secret string
	}{
		{
			"auth",
			errors.New("provider returned 401 Unauthorized: api_key=sk-abcdefghijklmnop password=hunter2secret99 user@example.com"),
			errors.New("provider returned 403 Forbidden: api_key=sk-zzzzzzzzzzzzzzzzzz"),
			"auth.invalid_key",
			"sk-abcdefghijklmnop",
		},
		{
			"quota",
			errors.New("provider returned 402 Payment Required: card 4111111111111111 was declined"),
			errors.New("配额已用尽, account owner-42@example.com"),
			"quota.exhausted",
			"4111111111111111",
		},
		{
			"rate limit",
			errors.New("provider returned 429 Too Many Requests: tenant super-secret-tenant"),
			errors.New("限流"),
			"rate_limit.exceeded",
			"super-secret-tenant",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			first := llm.ClassifyFailure(tc.first)
			second := llm.ClassifyFailure(tc.second)
			if first.ErrorID != tc.id || second.ErrorID != tc.id {
				t.Fatalf("ErrorID = %q / %q, want stable %q", first.ErrorID, second.ErrorID, tc.id)
			}
			if !safeID.MatchString(first.ErrorID) {
				t.Errorf("ErrorID %q is not a safe short identifier", first.ErrorID)
			}
			if len([]rune(first.ErrorID)) > 40 {
				t.Errorf("ErrorID %q is too long", first.ErrorID)
			}
			if regexp.MustCompile(regexp.QuoteMeta(tc.secret)).MatchString(first.ErrorID) {
				t.Errorf("ErrorID leaked secret %q: %q", tc.secret, first.ErrorID)
			}
			if first.UserAction != second.UserAction || first.Kind != second.Kind {
				t.Errorf("same family should route identically: %+v vs %+v", first, second)
			}
		})
	}
}

func TestClassifyFailureNeverCarriesDetail(t *testing.T) {
	// 整个结构体必须来自固定表；任何提供方文本都不得
	// 进入任何字段，包括内容任意的外部错误。
	errs := []error{
		errors.New("provider returned 401 Unauthorized: api_key=sk-abcdefghijklmnop"),
		errors.New("unknown gateway failure with trace 0123456789abcdef0123456789abcdef"),
		errors.New("完全未知的错误 细节不应外传"),
	}
	for _, err := range errs {
		info := llm.ClassifyFailure(err)
		if !regexp.MustCompile(`^[a-z_]+(\.[a-z_]+)?$`).MatchString(info.ErrorID) {
			t.Errorf("ErrorID %q is not a fixed identifier (%v)", info.ErrorID, err)
		}
		if string(info.Kind) == "" || string(info.UserAction) == "" {
			t.Errorf("incomplete classification %+v for %v", info, err)
		}
	}
}

func TestClassifyFailureMultilingual(t *testing.T) {
	cases := []struct {
		name string
		err  error
		kind llm.FailureKind
	}{
		{"zh config", errors.New("尚未配置 API Key"), llm.FailureConfig},
		{"zh auth", errors.New("认证失败，登录已过期"), llm.FailureAuth},
		{"zh quota", errors.New("配额已用尽"), llm.FailureQuota},
		{"zh rate limit", errors.New("请求过于频繁，限流"), llm.FailureRateLimit},
		{"zh content filter", errors.New("内容审核未通过，安全策略拦截"), llm.FailureAgentAction},
		{"zh context overflow", errors.New("上下文窗口已用尽"), llm.FailureContextOverflow},
		{"zh model", errors.New("模型不存在"), llm.FailureConfig},
		{"zh network", errors.New("网络连接失败"), llm.FailureTransient},
		{"zh server", errors.New("服务不可用"), llm.FailureTransient},
		{"unmatched foreign language", errors.New("認証エラー"), llm.FailureUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := llm.ClassifyFailure(tc.err).Kind; got != tc.kind {
				t.Errorf("kind = %q, want %q for %q", got, tc.kind, tc.err)
			}
		})
	}
}

func TestFailureVocabularyIsClosed(t *testing.T) {
	kinds := []llm.FailureKind{
		llm.FailureAuth, llm.FailureQuota, llm.FailureRateLimit, llm.FailureConfig,
		llm.FailureTransient, llm.FailureAgentAction, llm.FailureContextOverflow,
		llm.FailureInternal, llm.FailureUnknown,
	}
	seen := map[llm.FailureKind]bool{}
	for _, k := range kinds {
		if k == "" || seen[k] {
			t.Errorf("duplicate or empty FailureKind %q", k)
		}
		seen[k] = true
	}
	if len(seen) != 9 {
		t.Errorf("closed vocabulary should have 9 kinds, got %d", len(seen))
	}

	actions := []llm.UserAction{llm.UserActionNone, llm.UserActionRetry, llm.UserActionSettings}
	seenAction := map[llm.UserAction]bool{}
	for _, a := range actions {
		if a == "" || seenAction[a] {
			t.Errorf("duplicate or empty UserAction %q", a)
		}
		seenAction[a] = true
	}
	if len(seenAction) != 3 {
		t.Errorf("closed vocabulary should have 3 actions, got %d", len(seenAction))
	}
}

func TestClassifyFailureContextOverflowIsNotRetryable(t *testing.T) {
	// 超大的上下文不会因重放而消除：失败必须是
	// Retryable=false（不自动重试），同时仍是一种调用方
	// 可用来触发压缩的独立类别。ClassifyRetryError 也认同。
	want := llm.FailureInfo{llm.FailureContextOverflow, false, llm.UserActionSettings, "context_overflow.exceeded"}
	cases := []struct {
		name string
		err  error
	}{
		{"anthropic", errors.New("prompt is too long: 250000 tokens > 200000 tokens")},
		{"openai", errors.New("provider returned 400 Bad Request: context_length_exceeded")},
		{"cjk", errors.New("上下文窗口已用尽")},
		{"actionable", llm.ClassifyAPIError(errors.New("context window exceeded"))},
		{"wrapped", fmt.Errorf("chat: %w", errors.New("context window exceeded"))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := llm.ClassifyFailure(tc.err)
			if got != want {
				t.Errorf("ClassifyFailure = %+v, want %+v", got, want)
			}
			reason := llm.ClassifyRetryError(tc.err)
			if reason != llm.ReasonNone {
				t.Errorf("ClassifyRetryError = %q, want ReasonNone (non-retryable)", reason)
			}
			if got.Retryable != (reason != llm.ReasonNone) {
				t.Errorf("Retryable = %v disagrees with ClassifyRetryError %q", got.Retryable, reason)
			}
		})
	}
}

func TestClassifyFailureContextOverflowErrorIDStableAndRedacted(t *testing.T) {
	// 错误 ID 是固定的表值：跨提供方措辞、
	// 语言和凭据都相同，且从不携带原始错误文本。
	first := llm.ClassifyFailure(errors.New("provider returned 400 Bad Request: prompt is too long api_key=sk-abcdefghijklmnop"))
	second := llm.ClassifyFailure(errors.New("上下文窗口已用尽, user@example.com"))
	if first.ErrorID != "context_overflow.exceeded" || second.ErrorID != first.ErrorID {
		t.Fatalf("ErrorID = %q / %q, want stable %q", first.ErrorID, second.ErrorID, "context_overflow.exceeded")
	}
	if !regexp.MustCompile(`^(?:unknown|[a-z_]+(?:\.[a-z_]+)?)$`).MatchString(first.ErrorID) {
		t.Errorf("ErrorID %q is not a safe short identifier", first.ErrorID)
	}
	for _, raw := range []string{"sk-abcdefghijklmnop", "user@example.com", "prompt is too long", "上下文"} {
		if strings.Contains(first.ErrorID, raw) || strings.Contains(second.ErrorID, raw) {
			t.Errorf("ErrorID leaked raw text %q: %q / %q", raw, first.ErrorID, second.ErrorID)
		}
	}
}
