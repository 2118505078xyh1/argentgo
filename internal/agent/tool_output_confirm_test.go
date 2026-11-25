package agent

import (
	"context"
	"strings"
	"testing"

	"example.com/argentgo/internal/llm"
)

// TestShouldConfirmToolOutput 覆盖纯判定函数在敏感命令、普通命令、阈值边界、
// CJK 文本和空输入下的行为。所有用例都不发事件、不阻塞，因此可直接并行断言。
func TestShouldConfirmToolOutput(t *testing.T) {
	large := strings.Repeat("x", toolOutputConfirmMinBytes)
	below := strings.Repeat("x", toolOutputConfirmMinBytes-1)
	// 每个 CJK 字符占 3 字节，故该输出按字节数远超阈值。
	largeCJK := strings.Repeat("密钥", toolOutputConfirmMinBytes)
	withKey := strings.Repeat("x", toolOutputConfirmMinBytes) + "-----BEGIN PRIVATE KEY-----"

	tests := []struct {
		name   string
		tool   string
		output string
		bytes  int
		want   bool
	}{
		{"敏感命令 env 大输出", "env", large, 0, true},
		{"敏感命令 printenv 大输出", "printenv", large, 0, true},
		{"敏感命令 cat 大输出", "cat", large, 0, true},
		{"命令名带空白与大小写归一", "  ENV  ", large, 0, true},
		{"普通命令 ls 大输出", "ls", large, 0, false},
		{"普通命令 echo 大输出", "echo", large, 0, false},
		{"恰好达到阈值", "env", large, 0, true},
		{"低于阈值一个字节", "env", below, 0, false},
		{"显式字节数触发大输出", "env", "short", toolOutputConfirmMinBytes, true},
		{"显式字节数覆盖大文本", "env", large, 10, false},
		{"CJK 大输出来自敏感命令", "cat", largeCJK, 0, true},
		{"CJK 大输出来自普通命令", "ls", largeCJK, 0, false},
		{"CJK 文本显式小字节数", "cat", largeCJK, 3, false},
		{"通用 shell 输出含私钥标记", "bash", withKey, 0, true},
		{"空命令名与空输出", "", "", 0, false},
		{"敏感命令但输出为空", "env", "", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldConfirmToolOutput(tc.tool, tc.output, tc.bytes); got != tc.want {
				t.Fatalf("shouldConfirmToolOutput(%q, len=%d, bytes=%d) = %v, want %v",
					tc.tool, len(tc.output), tc.bytes, got, tc.want)
			}
		})
	}
}

// TestToolOutputConfirmationFlag 验证 opt-in 开关的默认值与切换语义。
func TestToolOutputConfirmationFlag(t *testing.T) {
	SetToolOutputConfirmation(false)
	defer SetToolOutputConfirmation(false)
	if toolOutputConfirmationEnabled() {
		t.Fatal("默认应关闭输出二次确认")
	}
	SetToolOutputConfirmation(true)
	if !toolOutputConfirmationEnabled() {
		t.Fatal("设置后应开启输出二次确认")
	}
}

// TestConfirmToolOutputBeforeAppendDefaultOff 验证默认关闭时是即时空操作：
// 既不询问审批器，也不改动输出字节。
func TestConfirmToolOutputBeforeAppendDefaultOff(t *testing.T) {
	SetToolOutputConfirmation(false)
	defer SetToolOutputConfirmation(false)
	asked := 0
	s := newRunState(Options{
		Approve: approverFunc(func(context.Context, string, string) (bool, error) {
			asked++
			return false, nil
		}),
	}, "test", nil)
	out := strings.Repeat("x", toolOutputConfirmMinBytes)
	got := s.confirmToolOutputBeforeAppend(context.Background(), toolCall("c1", "env", "{}"), out)
	if got != out {
		t.Fatal("默认关闭时输出应逐字节保持原样")
	}
	if asked != 0 {
		t.Fatalf("默认关闭时不应询问审批器，实际询问 %d 次", asked)
	}
}

// TestConfirmToolOutputBeforeAppendGate 验证 opt-in 开启后的门控：
// 敏感大输出被拒绝时用不含原始输出的占位文本替换；批准时原样放行；
// 非敏感输出不触发审批；未配置审批器时 fail-closed；已取消的 context 不阻塞。
func TestConfirmToolOutputBeforeAppendGate(t *testing.T) {
	SetToolOutputConfirmation(true)
	defer SetToolOutputConfirmation(false)
	large := strings.Repeat("x", toolOutputConfirmMinBytes)

	t.Run("拒绝时不泄漏原始输出", func(t *testing.T) {
		asked := 0
		s := newRunState(Options{
			Approve: approverFunc(func(context.Context, string, string) (bool, error) {
				asked++
				return false, nil
			}),
		}, "test", nil)
		got := s.confirmToolOutputBeforeAppend(context.Background(), toolCall("c1", "env", "{}"), large)
		if asked != 1 {
			t.Fatalf("审批器询问次数 = %d, want 1", asked)
		}
		if got != toolOutputWithheldReason {
			t.Fatalf("拒绝后输出 = %q, want 占位文本", got)
		}
		if strings.Contains(got, large) {
			t.Fatal("拒绝后原始输出不应出现在对话中")
		}
	})

	t.Run("批准时原样放行", func(t *testing.T) {
		s := newRunState(Options{
			Approve: approverFunc(func(context.Context, string, string) (bool, error) { return true, nil }),
		}, "test", nil)
		got := s.confirmToolOutputBeforeAppend(context.Background(), toolCall("c2", "env", "{}"), large)
		if got != large {
			t.Fatal("批准后输出应逐字节保持原样")
		}
	})

	t.Run("非敏感输出不询问", func(t *testing.T) {
		asked := 0
		s := newRunState(Options{
			Approve: approverFunc(func(context.Context, string, string) (bool, error) {
				asked++
				return true, nil
			}),
		}, "test", nil)
		got := s.confirmToolOutputBeforeAppend(context.Background(), toolCall("c3", "ls", "{}"), large)
		if asked != 0 || got != large {
			t.Fatalf("非敏感输出应直接放行且无询问，asked=%d, 输出是否原样=%v", asked, got == large)
		}
	})

	t.Run("无审批器时 fail-closed", func(t *testing.T) {
		s := newRunState(Options{}, "test", nil)
		got := s.confirmToolOutputBeforeAppend(context.Background(), toolCall("c4", "env", "{}"), large)
		if got != toolOutputWithheldReason {
			t.Fatalf("无审批器时应扣住输出，got %q", got)
		}
	})

	t.Run("已取消的 context 不阻塞", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		s := newRunState(Options{}, "test", nil)
		got := s.confirmToolOutputBeforeAppend(ctx, toolCall("c5", "env", "{}"), large)
		if got != large {
			t.Fatal("已取消时不应进入确认路径，输出应原样返回")
		}
	})
}

// TestRunToolsWithholdsSensitiveOutput 是接线级验证：opt-in 开启后，串行派发路径
// 写回 messages 的是占位文本，而非工具产生的敏感大输出。
func TestRunToolsWithholdsSensitiveOutput(t *testing.T) {
	SetToolOutputConfirmation(true)
	defer SetToolOutputConfirmation(false)
	large := strings.Repeat("x", toolOutputConfirmMinBytes)
	ft := &fakeTools{run: func(context.Context, string, string) (string, error) { return large, nil }}
	s := newRunState(Options{
		Tools:   ft,
		Approve: approverFunc(func(context.Context, string, string) (bool, error) { return false, nil }),
	}, "test", nil)
	calls := []llm.ToolCall{toolCall("c1", "env", "{}")}
	s.runTools(context.Background(), 0, calls)
	got := lastToolContent(s.messages)
	if got != toolOutputWithheldReason {
		t.Fatalf("写回对话的输出 = %q, want 占位文本", got)
	}
}
