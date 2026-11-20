package session

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTitleGenerationPromptContainsInstructionAndMessage(t *testing.T) {
	prompt := titleGenerationPrompt("帮我写一个登录页面")
	if !strings.Contains(prompt, "用不超过 20 字概括对话主题") {
		t.Errorf("提示缺少标题字数指令: %q", prompt)
	}
	if !strings.Contains(prompt, "帮我写一个登录页面") {
		t.Errorf("提示未包含首条消息: %q", prompt)
	}
}

func TestTitleGenerationPromptTruncatesLongInputAtRuneBoundary(t *testing.T) {
	// 覆盖多字节 CJK 与四字节 emoji，确保截断不会落在 UTF-8 字符中间。
	cases := []struct {
		name     string
		unit     string
		overflow int
	}{
		{"CJK", "界", 50},
		{"emoji", "🙂", 30},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			long := strings.Repeat(tc.unit, titlePromptMessageLimit+tc.overflow)
			prompt := titleGenerationPrompt(long)
			wantMessage := truncateRunes(long, titlePromptMessageLimit)
			if !strings.HasSuffix(prompt, wantMessage) {
				t.Fatalf("提示未以截断后的消息结尾: %q", prompt)
			}
			if !utf8.ValidString(prompt) {
				t.Fatalf("提示包含非法 UTF-8，发生了字符截断: %q", prompt)
			}
			if got := len([]rune(wantMessage)); got != titlePromptMessageLimit {
				t.Fatalf("消息符文数 = %d, want %d", got, titlePromptMessageLimit)
			}
		})
	}
}

func TestTitleGenerationPromptRedactsSecrets(t *testing.T) {
	prompt := titleGenerationPrompt("token = 2118505078xyh1")
	if strings.Contains(prompt, "2118505078xyh1") {
		t.Fatalf("提示泄露了密钥: %q", prompt)
	}
	if !strings.Contains(prompt, "[redacted:token]") {
		t.Fatalf("提示缺少脱敏占位符: %q", prompt)
	}
}

func TestTitleGenerationPromptFoldsWhitespace(t *testing.T) {
	prompt := titleGenerationPrompt("  布置  任务\n下一行 ")
	wantMessage := "布置 任务 下一行"
	if !strings.HasSuffix(prompt, wantMessage) {
		t.Fatalf("提示未将消息折叠为单行: %q", prompt)
	}
	if strings.Contains(prompt, "\n下一行") {
		t.Fatalf("提示仍包含原始换行: %q", prompt)
	}
}

func TestTitleGenerationPromptTreatsBlankInputAsEmpty(t *testing.T) {
	empty := titleGenerationPrompt("")
	if !strings.Contains(empty, "用不超过 20 字概括对话主题") {
		t.Fatalf("空消息提示仍应包含指令: %q", empty)
	}
	for _, in := range []string{"   ", "\n\t", " \r\n "} {
		if got := titleGenerationPrompt(in); got != empty {
			t.Fatalf("空白输入 %q 应等价于空输入: got %q, want %q", in, got, empty)
		}
	}
}

func TestTitleGenerationPromptIsBoundedAndDeterministic(t *testing.T) {
	huge := strings.Repeat("界abc🙂", 5000)
	first := titleGenerationPrompt(huge)
	second := titleGenerationPrompt(huge)
	if first != second {
		t.Fatal("相同输入应得到相同提示")
	}
	base := len([]rune(titleGenerationPrompt("")))
	if n := len([]rune(first)); n > base+titlePromptMessageLimit {
		t.Fatalf("提示长度未受界: %d 符文, 上限 %d", n, base+titlePromptMessageLimit)
	}
}
