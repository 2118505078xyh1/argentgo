package session

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestFailureReasonKeepsExitCodeAfterLowercaseLengthChange 覆盖一个真实的
// 压缩 bug：failureReason 将整个工具输出小写化以定位
// 不区分大小写的 "[exit N]" 标记，然后用那个小写化后的字节偏移去
// 切分原始文本。strings.ToLower 按符文逐一折叠，因此标记之前
// 小写形式字节更少的符文（İ 2→1、ẞ 3→2、K 3→1）会把偏移推离
// 符文边界。结果是乱码尾部、前缀匹配失败，
// 以及——当没有其他失败标记时——失败被完全
// 从压缩摘要中丢弃。在切分原始字符串之前，偏移必须通过
// 符文索引映射回去。
func TestFailureReasonKeepsExitCodeAfterLowercaseLengthChange(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"ascii", "compile done [exit 1]"},
		{"dotted-capital-i", "İ done [exit 1]"}, // U+0130 -> i：2 字节 -> 1
		{"capital-sharp-s", "ẞ done [exit 1]"},  // U+1E9E -> ß：3 字节 -> 2
		{"kelvin-sign", "K done [exit 1]"},      // U+212A -> k：3 字节 -> 1
		{"cjk-unchanged", "编译完成 [exit 1]"},      // 中文小写化后不变
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason, ok := failureReason(tc.content)
			if !ok {
				t.Fatalf("failureReason(%q) found no failure", tc.content)
			}
			if reason != "[exit 1]" {
				t.Errorf("failureReason(%q) = %q, want %q", tc.content, reason, "[exit 1]")
			}
			if !utf8.ValidString(reason) {
				t.Errorf("failureReason(%q) returned invalid UTF-8 %q", tc.content, reason)
			}
		})
	}
}

// TestSummarizeKeepsFailureWithUnicodeBeforeExitCode 是端到端守护：
// 被丢弃的工具结果若在退出码之前带有长度会变化的符文，
// 仍必须作为失败出现在压缩摘要中，而不是
// 静默丢失恢复轮次所需的证据。
func TestSummarizeKeepsFailureWithUnicodeBeforeExitCode(t *testing.T) {
	dropped := []Entry{
		{Role: "tool", Name: "bash", Content: "K done [exit 2]"},
	}
	compaction := Summarize(dropped, 0)
	if !utf8.ValidString(compaction.Summary) {
		t.Fatalf("summary is not valid UTF-8: %q", compaction.Summary)
	}
	if !strings.Contains(compaction.Summary, "[exit 2]") {
		t.Fatalf("summary lost the exit-code failure: %q", compaction.Summary)
	}
	if len(compaction.Failures) != 1 || compaction.Failures[0] != "bash — [exit 2]" {
		t.Fatalf("Failures = %q, want [\"bash — [exit 2]\"]", compaction.Failures)
	}
}
