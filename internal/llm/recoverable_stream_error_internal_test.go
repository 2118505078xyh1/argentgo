package llm

import "testing"

// TestIsRecoverableStreamErrorRecognizesEnglishPhrases 覆盖 pi 审计列出的
// 英文流中断文案及其常见被动/同义形式：这些文本应纳入可重试判定。
func TestIsRecoverableStreamErrorRecognizesEnglishPhrases(t *testing.T) {
	messages := []string{
		"stream interrupted",
		"stream was interrupted",
		"stream got interrupted",
		"stream interruption detected",
		"stream disconnected",
		"stream was disconnected",
		"stream disconnect",
		"stream aborted",
		"stream dropped",
		"stream ended early",
		"stream ended unexpectedly",
		"stream ended without terminator",
		"stream cut short",
		"connection closed",
		"connection was closed",
		"connection close",
		"connection dropped",
		"session closed",
		"response cut short",
		"output was cut short",
		"the response was cut short abruptly",
		"cut short",
		"unexpected end of stream",
		"EOF of the stream",
		"incomplete stream",
		"upstream_stream_read_error",
		"UPSTREAM STREAM READ ERROR",
		"upstream-stream-read-error",
		"upstream stream read error",
	}
	for _, msg := range messages {
		t.Run(msg, func(t *testing.T) {
			if !isRecoverableStreamError(msg) {
				t.Fatalf("isRecoverableStreamError(%q) = false, want true", msg)
			}
		})
	}
}

// TestIsRecoverableStreamErrorRecognizesChinesePhrases 确认中文流中断文案同样命中，
// 包括"被/已/意外"等修饰语与"流式响应/输出/答复"等主体。
func TestIsRecoverableStreamErrorRecognizesChinesePhrases(t *testing.T) {
	messages := []string{
		"流中断",
		"流式响应中断",
		"流式输出中断",
		"流被意外断开",
		"流已断开",
		"响应被截断",
		"输出被截断",
		"答复中断",
		"响应中断，请重试",
		"连接已关闭",
		"连接被断开",
		"连接意外中断",
		"上游流读取错误",
		"上游流式读取失败",
		"上游读取异常",
	}
	for _, msg := range messages {
		t.Run(msg, func(t *testing.T) {
			if !isRecoverableStreamError(msg) {
				t.Fatalf("isRecoverableStreamError(%q) = false, want true", msg)
			}
		})
	}
}

// TestIsRecoverableStreamErrorIsCaseInsensitive 确认大小写不敏感：
// 全小写、全大写与混合形式都命中。
func TestIsRecoverableStreamErrorIsCaseInsensitive(t *testing.T) {
	messages := []string{
		"StReAm InTeRrUpTeD",
		"CONNECTION CLOSED",
		"UpStReAm_StReAm_ReAd_ErRoR",
		"ReSpOnSe CuT ShOrT",
	}
	for _, msg := range messages {
		t.Run(msg, func(t *testing.T) {
			if !isRecoverableStreamError(msg) {
				t.Fatalf("isRecoverableStreamError(%q) = false, want true", msg)
			}
		})
	}
}

// TestIsRecoverableStreamErrorEmptyInput 确认空串、纯空白与不含中断措辞的
// 普通文本都返回 false。
func TestIsRecoverableStreamErrorEmptyInput(t *testing.T) {
	for _, msg := range []string{"", "   ", "\t\n  \r\n", "nothing to see here", "ok"} {
		t.Run("empty/"+msg, func(t *testing.T) {
			if isRecoverableStreamError(msg) {
				t.Fatalf("isRecoverableStreamError(%q) = true, want false", msg)
			}
		})
	}
}

// TestIsRecoverableStreamErrorDoesNotMisreport 锁定误报边界：认证、配额、限流、
// 上下文溢出等普通错误，以及通用的流哨兵、流能力描述、非中断的连接错误
// （refused / timeout）、长度截断和普通关闭事件都不得命中。
func TestIsRecoverableStreamErrorDoesNotMisreport(t *testing.T) {
	cases := []struct {
		name string
		msg  string
	}{
		{name: "invalid api key", msg: "invalid api key provided"},
		{name: "auth failure", msg: "authentication failed: 401 unauthorized"},
		{name: "quota exhausted", msg: "insufficient_quota: your quota has been exhausted"},
		{name: "rate limited", msg: "rate limit exceeded, retry after 30s"},
		{name: "context overflow", msg: "context window exceeded, please shorten the prompt"},
		{name: "content filter", msg: "content blocked by safety filter"},
		{name: "model not found", msg: "model not found"},
		{name: "bad request", msg: "bad request 400"},
		{name: "plain failure", msg: "request failed"},
		{name: "unexpected error", msg: "an unexpected error occurred"},
		{name: "generic stream sentinel", msg: "provider stream error"},
		{name: "streaming supported", msg: "streaming is supported by this model"},
		{name: "stream of tokens", msg: "a stream of tokens"},
		{name: "connection refused", msg: "connection refused"},
		{name: "connection timeout", msg: "connection timed out"},
		{name: "io timeout", msg: "i/o timeout while reading"},
		{name: "connection established", msg: "connection established successfully"},
		{name: "issue closed", msg: "the issue was closed"},
		{name: "input truncated by limit", msg: "input truncated due to max tokens"},
		{name: "output truncated by length", msg: "output truncated because it exceeded the length limit"},
		{name: "partial response by tokens", msg: "partial response due to max output tokens"},
		{name: "shortcut identifier", msg: "shortcut key configuration updated"},
		{name: "cut shorty name", msg: "a nickname containing cut shorty inside"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if isRecoverableStreamError(tc.msg) {
				t.Fatalf("isRecoverableStreamError(%q) = true, want false", tc.msg)
			}
		})
	}
}

// TestIsRecoverableStreamErrorIsPure 确认函数是纯判断：重复调用结果一致，
// 且不修改传入的字符串。
func TestIsRecoverableStreamErrorIsPure(t *testing.T) {
	msg := "provider closed the stream while streaming (upstream_stream_read_error)"
	original := msg
	first := isRecoverableStreamError(msg)
	second := isRecoverableStreamError(msg)
	if !first || !second {
		t.Fatalf("isRecoverableStreamError = (%v, %v), want (true, true)", first, second)
	}
	if msg != original {
		t.Fatalf("input mutated: %q -> %q", original, msg)
	}
}
