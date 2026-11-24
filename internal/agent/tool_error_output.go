package agent

import (
	"strings"
	"sync/atomic"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

const (
	// toolErrorOutputMaxBytes 限制并入错误文本的工具输出字节数。
	// 工具输出通常很短；取工具错误反射预算的一半，既够呈现 stderr 根因，
	// 又不会让一次失败淹没上下文。
	toolErrorOutputMaxBytes = toolErrorReflectionMaxBytes / 2
)

// toolErrorOutputOptIn 控制是否把工具返回的部分输出（stderr/stdout 原文）
// 并入回灌给模型的错误文本。默认开启：仅 ARGENT_TOOL_ERROR_OUTPUT=0 时关闭。
//
// 背景：bash 等工具把真实失败原因放在返回的 output（如 "shell: cd: not a directory"）
// 里，而 err.Error() 只有 "bash: exit status 1"。若只回灌后者，不同失败命令会得到
// 逐字节相同的错误字符串，触发 dedupe 的 outputWouldBlock 误判为"伪进展循环"，
// 进而拦截后续所有工具调用，让模型陷入幻觉式空转。带上原文即可根除这类误判。
var toolErrorOutputOptIn atomic.Bool

func init() { toolErrorOutputOptIn.Store(true) }

// SetToolErrorOutput 打开或关闭错误输出并入接线（供 app 层按 env 设置）。
func SetToolErrorOutput(enabled bool) { toolErrorOutputOptIn.Store(enabled) }

func toolErrorOutputEnabled() bool { return toolErrorOutputOptIn.Load() }

// appendToolErrorOutput 把工具的部分输出并进基础错误文本，返回新的 errText。
//
// 纯函数：脱敏 + 有界裁剪 + 去重，只在输出含有效正文时追加；关闭门控、
// 输出为空、或基础错误已包含该正文时逐字节原样返回。
func appendToolErrorOutput(errText, output string) string {
	if !toolErrorOutputEnabled() {
		return errText
	}
	clean := strings.TrimSpace(llm.RedactText(output))
	if clean == "" || strings.Contains(errText, clean) {
		return errText
	}
	return errText + "\n" + clipVerifyTail(clean, toolErrorOutputMaxBytes)
}
