package agent

import (
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// shakeOffEnv 测试期间确保开关不被外部环境干扰。
func shakeOffEnv(t *testing.T) {
	t.Helper()
	t.Setenv("ARGENT_SHAKE", "1")
}

func TestShakeBlocks(t *testing.T) {
	shakeOffEnv(t)
	big := "```go\n" + strings.Repeat("func f() {}\n", 80) + "```"
	small := "```go\nfmt.Println(\"hi\")\n```"

	cases := []struct {
		name       string
		messages   []llm.Message
		protect    int // 保护窗口 rune 数；0 表示 4
		wantChange int // 期望被改写的消息下标，-1 表示无
		wantElided bool
	}{
		{
			name: "large fenced block outside protection is shaken",
			messages: []llm.Message{
				{Role: "system", Content: "sys"},
				{Role: "assistant", Content: big},
				{Role: "user", Content: "继续"},
				{Role: "assistant", Content: "done"},
			},
			wantChange: 1,
			wantElided: true,
		},
		{
			name: "short block unchanged",
			messages: []llm.Message{
				{Role: "system", Content: "sys"},
				{Role: "assistant", Content: small},
				{Role: "user", Content: "继续"},
			},
			wantChange: -1,
		},
		{
			name:    "protected window keeps recent big block",
			protect: 16,
			messages: []llm.Message{
				{Role: "system", Content: "sys"},
				{Role: "assistant", Content: big},
				{Role: "user", Content: "继续"},
			},
			wantChange: -1,
		},
		{
			name: "image and tool parts preserved",
			messages: []llm.Message{
				{Role: "system", Content: "sys"},
				{Role: "assistant", Content: big, ToolCalls: []llm.ToolCall{toolCall("c1", "read", "{}")}},
				{Role: "user", Content: "继续"},
			},
			wantChange: -1,
		},
		{
			name:    "top level xml block shaken",
			protect: 1,
			messages: []llm.Message{
				{Role: "system", Content: "sys"},
				{Role: "assistant", Content: "<data>\n" + strings.Repeat("<row>value</row>\n", 60) + "</data>"},
				{Role: "user", Content: "继续"},
			},
			wantChange: 1,
			wantElided: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			protect := tc.protect
			if protect == 0 {
				protect = 4
			}
			got, elided := ShakeBlocks(tc.messages, protect, 512)
			if len(got) != len(tc.messages) {
				t.Fatalf("message count changed: %d -> %d", len(tc.messages), len(got))
			}
			if tc.wantChange >= 0 {
				if got[tc.wantChange].Content == tc.messages[tc.wantChange].Content {
					t.Fatal("大块未被替换")
				}
				if !strings.Contains(got[tc.wantChange].Content, "[shaken:") {
					t.Fatalf("占位文本缺失: %s", got[tc.wantChange].Content)
				}
			} else {
				for i := range tc.messages {
					if got[i].Content != tc.messages[i].Content {
						t.Fatalf("消息 %d 不应被改写: %q -> %q", i, tc.messages[i].Content, got[i].Content)
					}
				}
			}
			if tc.wantElided != (elided > 0) {
				t.Fatalf("elided = %d, want positive=%v", elided, tc.wantElided)
			}
			// 入参不可变。
			if tc.messages[0].Content != "sys" {
				t.Fatal("入参被修改")
			}
		})
	}
}

func TestShakeBlocksCJKNotSplit(t *testing.T) {
	shakeOffEnv(t)
	content := "```\n" + strings.Repeat("中文内容测试\n", 100) + "```"
	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "assistant", Content: content},
		{Role: "user", Content: "继续"},
	}
	got, elided := ShakeBlocks(msgs, 1, 512)
	if elided <= 0 {
		t.Fatal("CJK 大块应被剥离")
	}
	if !strings.Contains(got[1].Content, "[shaken:") {
		t.Fatalf("缺少占位: %s", got[1].Content)
	}
}
