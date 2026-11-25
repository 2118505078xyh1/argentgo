package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"example.com/argentgo/internal/llm"
)

// 本文件守护 R16 工具错误反射接线：
//   - 默认关闭时，回灌给模型的 tool 消息必须与既有基线逐字节一致（接线前的行为不变）；
//   - 开启后，模型必须看到带修复指令的增强反馈。
//
// 文件隔离：这里只新增测试，不改生产代码（dispatch.go 与 tool_error_reflection.go
// 由主会话/其它代理负责接线与实现）。
//
// 依赖说明：dispatch.go 错误分支对 ReflectionForDispatch 的接线由主会话后续接入。
// 接线落盘前，"开启后增强"的断言落在接线原语 ReflectionForDispatch 本身；
// 接线落盘后，应把同样的断言扩展到模型可见的 tool 消息（取法见
// TestDispatchReflectionOffByteIdentical 中的 lastToolContent）。
// "默认关闭逐字节一致"则已经通过完整派发端到端守护，接线前后都成立。

// dispatchReflectionTestError 是一个可修复（reflectionWorthy 命中 "edit failed" /
// "anchor not found"）、但不是无效参数（不会触发参数熔断器）的脚本化工具错误，
// 用于稳定进入 runTool 的错误分支并让反射判定为真。
const dispatchReflectionTestError = "edit failed: anchor not found"

// runDispatchWithToolError 驱动一次完整派发：第一轮模型请求 toolName，
// 脚本化工具返回 rawErr，第二轮以文本收尾。返回结果与记录到的请求序列。
func runDispatchWithToolError(t *testing.T, toolName, rawErr string) (Result, *fakeChatter) {
	t.Helper()
	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec(toolName)}}
	tools.run = func(_ context.Context, _ string, _ string) (string, error) {
		return "", errors.New(rawErr)
	}
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			return toolReply(toolCall("c1", toolName, `{}`)), nil
		}
		return textReply("done"), nil
	}}
	runner := &Runner{Opts: Options{
		Model:    "m",
		Chatter:  chat,
		Tools:    tools,
		MaxTurns: 4,
	}}
	res, err := runner.Run(context.Background(), "investigate")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return res, chat
}

// TestDispatchReflectionOffByteIdentical 检查默认（SetToolErrorReflection(false)）时，
// 回灌给模型的 tool 消息与既有基线逐字节一致：既不含修复指令行，也不含错误头。
// 这是 opt-in 的核心承诺——接线不改变既有运行的任何字节。
func TestDispatchReflectionOffByteIdentical(t *testing.T) {
	defer SetToolErrorReflection(false)
	defer SetToolResultWrapping(false)
	defer SetToolOutputConfirmation(false)
	SetToolErrorReflection(false)
	SetToolResultWrapping(false)
	SetToolOutputConfirmation(false)

	const toolName = "edit"
	baseline := "error: " + dispatchReflectionTestError

	res, chat := runDispatchWithToolError(t, toolName, dispatchReflectionTestError)

	// 会话中的 tool 消息必须与基线逐字节相同。
	stored := lastToolContent(res.Messages)
	if stored != baseline {
		t.Fatalf("会话 tool 消息 = %q，期望逐字节基线 %q", stored, baseline)
	}
	if strings.Contains(stored, toolErrorFixInstruction) || strings.Contains(stored, "[tool: ") {
		t.Fatalf("默认关闭却注入了反射反馈：%q", stored)
	}

	// 模型实际看到的下一轮请求中的 tool 消息也必须与基线相同。
	seen := chat.requestsSeen()
	if len(seen) < 2 {
		t.Fatalf("请求轮次 = %d，期望至少两轮", len(seen))
	}
	modelSaw := lastToolContent(seen[1])
	if modelSaw != baseline {
		t.Fatalf("模型看到的 tool 消息 = %q，期望逐字节基线 %q", modelSaw, baseline)
	}
	if modelSaw != stored {
		t.Fatalf("模型看到的消息与会话记录不一致：seen=%q stored=%q", modelSaw, stored)
	}
}

// TestDispatchReflectionOnAugmentsError 检查开启反射后，一条可修复的工具错误
// 被包装成"修复指令行 + [tool: X] ERROR 头 + 错误正文"的增强反馈。
//
// 依赖说明：dispatch.go 错误分支对 ReflectionForDispatch 的接线由主会话后续接入。
// 接线落盘前，本测试直接验证接线原语 ReflectionForDispatch 的增强效果；
// 接线落盘后，应把同样的断言作用于模型可见的 tool 消息（取法见
// TestDispatchReflectionOffByteIdentical 中的 lastToolContent）。
func TestDispatchReflectionOnAugmentsError(t *testing.T) {
	defer SetToolErrorReflection(false)
	SetToolErrorReflection(true)

	const toolName = "edit"
	baseline := "error: " + dispatchReflectionTestError

	got := ReflectionForDispatch(toolName, baseline)

	if !strings.HasPrefix(got, toolErrorFixInstruction+"\n") {
		t.Fatalf("反馈 = %q，期望以修复指令行开头", got)
	}
	if !strings.Contains(got, "[tool: EDIT] ERROR") {
		t.Fatalf("反馈 = %q，期望含大写标签的错误头", got)
	}
	if !strings.Contains(got, dispatchReflectionTestError) {
		t.Fatalf("反馈 = %q，期望保留错误正文", got)
	}
	if got == baseline {
		t.Fatalf("开启反射后反馈仍与基线逐字节相同：%q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("反馈不是合法 UTF-8：%q", got)
	}
}

// TestDispatchReflectionRedactionBoundAndCJK 检查接线原语在脱敏、长度有界与 CJK 安全
// 三方面的守护（与 tool_reflection_wire_test.go 同口径）：增强反馈不得泄漏凭据、
// 不得撑爆上下文，且裁剪切点不得落在多字节 rune 中间。
func TestDispatchReflectionRedactionBoundAndCJK(t *testing.T) {
	defer SetToolErrorReflection(false)
	SetToolErrorReflection(true)

	t.Run("redacts secrets", func(t *testing.T) {
		// "bad arguments" 命中可修复标记，确保触发反射；凭据形态的文本会被脱敏。
		got := ReflectionForDispatch("edit", "error: bad arguments: api_key=sk_live_abcdefghijklmnop failed")
		if strings.Contains(got, "sk_live_abcdefghijklmnop") {
			t.Fatalf("反馈泄漏凭据：%q", got)
		}
		if !strings.Contains(got, "[redacted]") {
			t.Fatalf("反馈 = %q，期望含脱敏标记", got)
		}
	})

	t.Run("bounded", func(t *testing.T) {
		// "bad arguments" 是可修复标记；正文超长以覆盖裁剪。
		out := "error: bad arguments: " + strings.Repeat("Z", toolErrorReflectionMaxBytes+500)
		got := ReflectionForDispatch("edit", out)
		prefix := toolErrorFixInstruction + "\n[tool: EDIT] ERROR\n"
		if !strings.HasPrefix(got, prefix) {
			t.Fatalf("反馈 = %q，期望前缀 %q", got, prefix)
		}
		body := strings.TrimPrefix(got, prefix)
		if len(body) > toolErrorReflectionMaxBytes {
			t.Fatalf("正文 = %d 字节，期望 <= %d", len(body), toolErrorReflectionMaxBytes)
		}
		if !strings.HasPrefix(body, verifyTruncatedMarker) {
			t.Fatalf("正文 = %q，期望截断标记", body)
		}
		if !utf8.ValidString(got) {
			t.Fatalf("反馈不是合法 UTF-8：%q", got)
		}
	})

	t.Run("cjk safe", func(t *testing.T) {
		// "编辑失败" 是可修复标记；中文正文超长以覆盖裁剪切点。
		out := "error: 编辑失败：" + strings.Repeat("锚点界", toolErrorReflectionMaxBytes) + "END_TAIL"
		got := ReflectionForDispatch("编辑", out)
		if !utf8.ValidString(got) {
			t.Fatalf("反馈不是合法 UTF-8：%q", got)
		}
		if !strings.Contains(got, "[tool: 编辑] ERROR") {
			t.Fatalf("反馈 = %q，期望保留中文标签", got)
		}
	})
}
