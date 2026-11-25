package llm_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"example.com/argentgo/internal/llm"
)

// TestClassifyAPIErrorAuthSpacedPhrasing 覆盖真实世界的凭据错误，
// 它们在 "api key" 与判定语之间插入了几个词。旧的
// authRE 只匹配相邻形式（"invalid api key"、"api_key=..."），
// 因此 OpenAI 的 "Incorrect API key provided" 和同样常见的 "Your API
// key is invalid" 会落入 FamilyUnknown，其 UserAction 为 "none"——
// 用户从未被引导去重新登录。
func TestClassifyAPIErrorAuthSpacedPhrasing(t *testing.T) {
	auth := []string{
		"Incorrect API key provided: sk-proj-abcdefghijklmnop",
		"Your API key is invalid",
		"api key is incorrect",
		"API key has been revoked",
		"The api-key was unknown",
		"invalid api key",
	}
	for _, msg := range auth {
		t.Run("auth: "+msg, func(t *testing.T) {
			got := llm.ClassifyAPIError(errors.New(msg))
			if got == nil || got.Family != llm.FamilyAuth {
				t.Fatalf("ClassifyAPIError(%q) = %#v, want %q", msg, got, llm.FamilyAuth)
			}
			if !strings.Contains(got.NextStep, "登录") {
				t.Errorf("NextStep %q should point the user to re-login", got.NextStep)
			}
		})
	}

	// 新的可选分支不得仅仅因为提到 api key，就把 config、quota 或限流失败
	// 拉入 auth 家族。
	others := []struct {
		msg  string
		want string
	}{
		{"no api key configured", llm.FamilyNotConfigured},
		{"api key is required to use this endpoint", llm.FamilyNotConfigured},
		{"provider returned 429 Too Many Requests: rate limit exceeded for api key foo", llm.FamilyRateLimit},
		{"provider returned 402 Payment Required: insufficient quota for api key", llm.FamilyQuotaExhausted},
	}
	for _, tc := range others {
		t.Run("not auth: "+tc.msg, func(t *testing.T) {
			got := llm.ClassifyAPIError(errors.New(tc.msg))
			if got == nil || got.Family != tc.want {
				t.Fatalf("ClassifyAPIError(%q) = %#v, want %q", tc.msg, got, tc.want)
			}
		})
	}
}

// TestClassifyAPIErrorIsIdempotentThroughWrappers 检查文档记录的
// 约定：对错误分类两次会得到相同结果。直接
// 类型断言只识别未包装的 *ActionableError；用 %w 包装它的线客户端
// 会得到一个重新派生的副本，其细节被
// 重复（"额度或配额已用尽: chat: 额度或配额已用尽: ..."），而且当消息
// 不再携带原始信号时，其家族可能改变。
func TestClassifyAPIErrorIsIdempotentThroughWrappers(t *testing.T) {
	for _, baseErr := range []error{
		errors.New("provider returned 402 Payment Required: insufficient quota"),
		fmt.Errorf("%w: read stream: connection reset", llm.ErrStreamError),
	} {
		first := llm.ClassifyAPIError(baseErr)
		wrapped := fmt.Errorf("agent: call failed: %w", first)
		second := llm.ClassifyAPIError(wrapped)
		if second != first {
			t.Fatalf("reclassifying wrapped %q returned %#v, want the same instance %#v", first.Family, second, first)
		}
		if second.Family != first.Family || second.NextStep != first.NextStep {
			t.Fatalf("wrapped classification changed: %#v -> %#v", first, second)
		}
	}

	// 调用方构造的 ActionableError 保留自己的家族，即使其
	// 消息中没有任何可从文本重现它的模式。
	custom := &llm.ActionableError{Family: "custom_family", Message: "opaque upstream failure", NextStep: "contact support"}
	if got := llm.ClassifyAPIError(fmt.Errorf("wrapped: %w", custom)); got != custom || got.Family != "custom_family" {
		t.Fatalf("wrapped custom ActionableError = %#v, want the original", got)
	}
}
