package llm

import "testing"

// TestIsByteSizeOverflow 覆盖字节级请求体溢出的各类文案、
// 大小写与中文变体，以及与 token/输出上限等相邻概念的误报边界。
func TestIsByteSizeOverflow(t *testing.T) {
	overflow := []string{
		"request body too large",
		"provider returned 400 Bad Request: request body too large",
		"REQUEST BODY TOO LARGE",
		"Request Entity Too Large",
		"payload too large",
		"request payload is too large",
		"RequestSize 1048576 Limit 65536",
		"request_size_limit exceeded",
		"request size exceeds the maximum allowed size",
		"HTTP 413 Payload Too Large",
		"status 413: request entity too large",
		"413 request size limit exceeded",
		"size limit 413",
		"请求体过大",
		"请求正文超过大小限制",
		"请求大小超出上限",
		"载荷过大",
		"报文太大",
	}
	for _, msg := range overflow {
		t.Run("overflow/"+msg, func(t *testing.T) {
			if !isByteSizeOverflow(msg) {
				t.Errorf("isByteSizeOverflow(%q) = false, want true", msg)
			}
		})
	}

	notOverflow := []string{
		"",
		"max_tokens must be less than or equal to 4096",
		"invalid_request_error: max_tokens must be a number, got large",
		"maximum context length is 4097 tokens",
		"context window exceeded",
		"tokens exceeds the limit",
		"output token limit exceeded, reduce max_tokens",
		"response too long",
		"response body too large",
		"quota exceeded",
		"413",
		"HTTP status 413 occurred",
		"request body field is required",
		"request size: 100 bytes",
		"some unrelated failure",
		"请求参数无效",
		"上下文窗口已用尽",
		"rate limit exceeded",
	}
	for _, msg := range notOverflow {
		t.Run("not/"+msg, func(t *testing.T) {
			if isByteSizeOverflow(msg) {
				t.Errorf("isByteSizeOverflow(%q) = true, want false", msg)
			}
		})
	}
}

// TestIsByteSizeOverflowCaseInsensitive 确认同一文案的大小写变体结果一致。
func TestIsByteSizeOverflowCaseInsensitive(t *testing.T) {
	for _, msg := range []string{
		"request body too large",
		"REQUEST BODY TOO LARGE",
		"ReQuEsT sIzE LiMiT ExCeEdEd",
	} {
		if !isByteSizeOverflow(msg) {
			t.Errorf("isByteSizeOverflow(%q) = false, want true (case-insensitive)", msg)
		}
	}
}

// TestIsByteSizeOverflowEmpty 确认空串不会命中任何模式。
func TestIsByteSizeOverflowEmpty(t *testing.T) {
	if isByteSizeOverflow("") {
		t.Error("isByteSizeOverflow(\"\") = true, want false")
	}
}
