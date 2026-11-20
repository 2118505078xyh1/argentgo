package session

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestFailureReasonRecognizesExitMarkerInAnyCase 覆盖一个真实的压缩 bug：
// failureReason 以不区分大小写的方式定位 "[exit N]"，但随后从
// 原始文本中剥离了字面小写前缀。因此，打印
// "[EXIT 1]"（或 "[Exit 2]"）的 shell 解析不出数字，失败
// 会从压缩摘要中丢失，尽管标记已经被找到了。
// 数字必须从小写化后的字符串中读取，那里大小写折叠不会
// 改变 ASCII 数字，同时返回的文本保留原始拼写。
func TestFailureReasonRecognizesExitMarkerInAnyCase(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{"lowercase", "compile done [exit 1]", "[exit 1]"},
		{"uppercase", "compile done [EXIT 1]", "[EXIT 1]"},
		{"mixed case", "compile done [Exit 2]", "[Exit 2]"},
		{"uppercase after unicode", "编译完成 [EXIT 3]", "[EXIT 3]"},
		{"unicode before marker", "K done [EXIT 4]", "[EXIT 4]"},
		{"dotted capital i before marker", "İ done [EXIT 5]", "[EXIT 5]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason, ok := failureReason(tc.content)
			if !ok {
				t.Fatalf("failureReason(%q) found no failure", tc.content)
			}
			if reason != tc.want {
				t.Errorf("failureReason(%q) = %q, want %q", tc.content, reason, tc.want)
			}
			if !utf8.ValidString(reason) {
				t.Errorf("failureReason(%q) returned invalid UTF-8 %q", tc.content, reason)
			}
		})
	}
}

// TestFailureReasonStillRejectsSuccessfulAndMalformedExitMarkers 守护修复的
// 否定面：只有非零数字退出码才算失败，
// 与标记的大小写无关。
func TestFailureReasonStillRejectsSuccessfulAndMalformedExitMarkers(t *testing.T) {
	for _, content := range []string{
		"compile done [EXIT 0]",  // 成功
		"compile done [Exit 00]", // 带前导零的零
		"compile done [EXIT x]",  // 非数字
		"compile done [EXIT -1]", // 负数
	} {
		if reason, ok := failureReason(content); ok {
			t.Errorf("failureReason(%q) = %q, true; want no failure", content, reason)
		}
	}
}

// TestSummarizeKeepsUppercaseExitFailure 是端到端的守护：被丢弃的工具结果
// 若输出带有大写退出标记，必须仍能作为失败出现在
// 压缩摘要中，使恢复的轮次不会忘记它。
func TestSummarizeKeepsUppercaseExitFailure(t *testing.T) {
	dropped := []Entry{
		{Role: "tool", Name: "bash", Content: "[EXIT 7]"},
	}
	compaction := Summarize(dropped, 0)
	if !utf8.ValidString(compaction.Summary) {
		t.Fatalf("summary is not valid UTF-8: %q", compaction.Summary)
	}
	if !strings.Contains(compaction.Summary, "[EXIT 7]") {
		t.Fatalf("summary lost the upper-case exit failure: %q", compaction.Summary)
	}
	want := "bash — [EXIT 7]"
	if len(compaction.Failures) != 1 || compaction.Failures[0] != want {
		t.Fatalf("Failures = %q, want [%q]", compaction.Failures, want)
	}
}
