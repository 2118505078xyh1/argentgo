package llm

import "testing"

// TestIsProviderHardLimitRecognizesDedicatedCodes 覆盖 pi 审计列出的
// 供应方专属硬额度码：这些码应立即停止重试。
func TestIsProviderHardLimitRecognizesDedicatedCodes(t *testing.T) {
	cases := []struct {
		name string
		msg  string
	}{
		{
			name: "go usage limit error type",
			msg:  `{"type":"error","error":{"type":"GoUsageLimitError","message":"Weekly usage limit reached"}}`,
		},
		{
			name: "go usage limit wire text",
			msg:  `429 Weekly usage limit reached. type=GoUsageLimitError retry-after-ms=3242000`,
		},
		{
			name: "free usage limit error type",
			msg:  `{"error":{"type":"FreeUsageLimitError","message":"Free tier usage limit reached"}}`,
		},
		{
			name: "subscription sharing usage limit",
			msg:  `OpenAI API error (429): {"code":"subscription_sharing_usage_limit_exceeded","message":"Usage limit reached."}`,
		},
		{
			name: "subscription sharing workplace usage limit",
			msg:  `OpenAI API error (429): {"code":"subscription_sharing_workplace_individual_usage_limit_exceeded","message":"Usage limit reached."}`,
		},
		{
			name: "zhipu numeric json code",
			msg:  `{"code":1308,"message":"Usage limit reached for 5 hour"}`,
		},
		{
			name: "zhipu string json code",
			msg:  `{"code":"1308","message":"Usage limit reached for 5 hour"}`,
		},
		{
			name: "zhipu type equals",
			msg:  `429 (type=1308) usage limit reached`,
		},
		{
			name: "zhipu bracket code",
			msg:  `429 [1308][Usage limit reached for 5 hour. Your limit will reset at 13:23 UTC]`,
		},
		{
			name: "zhipu code text",
			msg:  `429 code 1308, Usage limit reached for 5 hour. Your limit will reset at 13:23 UTC`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !isProviderHardLimit(tc.msg) {
				t.Fatalf("isProviderHardLimit(%q) = false, want true", tc.msg)
			}
		})
	}
}

// TestIsProviderHardLimitRecognizesChineseZhipu 确认 Zhipu 1308 的常见
// 中文文案同样被识别。文案中的时间戳和换行不得妨碍 type=1308 命中。
func TestIsProviderHardLimitRecognizesChineseZhipu(t *testing.T) {
	messages := []string{
		"429 已达到 5 小时的使用上限。您的限额将在 2026-08-06 20:06:00 重置。\n已达到 5 小时的使用上限。您的限额将在 2026-08-06 20:06:00 重置。 (type=1308)",
		"429 已达到 5 小时的使用上限，您的限额将在 2026-08-06 20:06:00 重置. (type=1308)",
		"已达到 5 小时的使用上限，代码 1308",
	}
	for _, msg := range messages {
		t.Run(msg, func(t *testing.T) {
			if !isProviderHardLimit(msg) {
				t.Fatalf("isProviderHardLimit(%q) = false, want true", msg)
			}
		})
	}
}

// TestIsProviderHardLimitIsCaseInsensitive 确认大小写不敏感：
// 专属码的驼峰、全小写、全大写与混合形式都命中。
func TestIsProviderHardLimitIsCaseInsensitive(t *testing.T) {
	messages := []string{
		"gousagelimiterror",
		"GOUSAGELIMITERROR",
		"GoUsAgELiMiTErRoR",
		"freeusagelimiterror",
		"FREEUSAGELIMITERROR",
		"SUBSCRIPTION_SHARING_USAGE_LIMIT_EXCEEDED",
		"Subscription_Sharing_Usage_Limit_Exceeded",
	}
	for _, msg := range messages {
		t.Run(msg, func(t *testing.T) {
			if !isProviderHardLimit(msg) {
				t.Fatalf("isProviderHardLimit(%q) = false, want true", msg)
			}
		})
	}
}

// TestIsProviderHardLimitEmptyInput 确认空串、纯空白与不含专属码的
// 普通文本都返回 false。
func TestIsProviderHardLimitEmptyInput(t *testing.T) {
	for _, msg := range []string{"", "   ", "\t\n  \r\n", "no dedicated limit code here"} {
		t.Run("empty/"+msg, func(t *testing.T) {
			if isProviderHardLimit(msg) {
				t.Fatalf("isProviderHardLimit(%q) = true, want false", msg)
			}
		})
	}
}

// TestIsProviderHardLimitDoesNotMisreport 锁定误报边界：普通配额/限流
// 提示、通用配额错误码、可重试的 subscription_sharing 临时错误，以及
// 出现在标识符/时间戳/字节数里的裸 1308 都不得命中。
func TestIsProviderHardLimitDoesNotMisreport(t *testing.T) {
	cases := []struct {
		name string
		msg  string
	}{
		{
			name: "zh transient quota reset",
			msg:  `{"error":{"message":"请求过于频繁，配额将在 30 秒后重置，请稍后重试"}}`,
		},
		{
			name: "en transient rate limit",
			msg:  `rate limit exceeded, retry after 30s`,
		},
		{
			name: "plain usage limit without dedicated code",
			msg:  `{"error":{"message":"usage limit will reset in 30 seconds"}}`,
		},
		{
			name: "generic quota code left to existing classifier",
			msg:  `{"error":{"code":"insufficient_quota"}}`,
		},
		{
			name: "generic quota exceeded left to existing classifier",
			msg:  `quota exceeded, please slow down`,
		},
		{
			name: "subscription sharing usage unavailable is transient",
			msg:  `subscription_sharing_usage_unavailable: Usage cannot be checked.`,
		},
		{
			name: "subscription sharing user unavailable is transient",
			msg:  `subscription_sharing_user_unavailable: User cannot be loaded.`,
		},
		{
			name: "bare 1308 in request id",
			msg:  `request_id abc1308def`,
		},
		{
			name: "bare 1308 in latency",
			msg:  `latency 1308ms`,
		},
		{
			name: "bare 1308 in byte count",
			msg:  `{"size":1308,"status":"ok"}`,
		},
		{
			name: "1308 with trailing digit in json code",
			msg:  `{"code":13080,"message":"ok"}`,
		},
		{
			name: "1308 with trailing digit after type",
			msg:  `type=13080 something transient`,
		},
		{
			name: "go usage limit inside a larger word",
			msg:  `the congo usage limit is high`,
		},
		{
			name: "dedicated code inside a redacted credential value",
			msg:  `api_key=gousagelimit_secret_value1234567890`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if isProviderHardLimit(tc.msg) {
				t.Fatalf("isProviderHardLimit(%q) = true, want false", tc.msg)
			}
		})
	}
}

// TestIsProviderHardLimitIsPure 确认函数是纯判断：重复调用结果一致，
// 且不修改传入的字符串。
func TestIsProviderHardLimitIsPure(t *testing.T) {
	msg := `{"error":{"type":"GoUsageLimitError"}}`
	original := msg
	first := isProviderHardLimit(msg)
	second := isProviderHardLimit(msg)
	if !first || !second {
		t.Fatalf("isProviderHardLimit = (%v, %v), want (true, true)", first, second)
	}
	if msg != original {
		t.Fatalf("input mutated: %q -> %q", original, msg)
	}
}
