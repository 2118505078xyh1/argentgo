package main

import (
	"regexp"
	"strings"
)

// engine 错误在原生窗口中需要自己的呈现策略。
//
// TUI 在把流错误放入 transcript 之前对其进行限制、折叠和脱敏
// （internal/tui.StreamErrorEntry）。原生过去直接粘贴
// 任意长、多行且携带凭据，这既破坏单气泡布局，又把无界字符串钉在
// transcript 中直到会话结束。本文件在本地保持相同策略，使原生保持
// 对 engine 包无依赖。
// 凭据字段和 provider 错误中众所周知的关键前缀。

const (
	engineErrorPrefix    = "⚠ 引擎错误: "
	maxEngineErrorDetail = 200 // runes, including the truncation ellipsis
)

var (
	// 字段规则末尾的 [:=] 要求避免把
	// 这样的普通散文误判。
	// "token 已过期" from being rewritten.
	engineCredentialFieldRE = regexp.MustCompile(`(?i)(api[\s_\-]*key|access[\s_\-]*token|refresh[\s_\-]*token|authorization|bearer|password|passwd|secret|token)(["']?\s*[:=]\s*["']?)([^\s"',;}]+)`)
	engineKnownSecretRE     = regexp.MustCompile(`(?i)\b(?:sk|rk|pk|xoxb|ghp|gho|github_pat)[-_][A-Za-z0-9_\-]{12,}\b`)
)

// collapseEngineErrorSpace 把每个空白段（包括换行和制表符）折叠为一个空格，
// 使一个错误总是渲染为一行。
// 一行，且不能引入假的 Markdown 块。
func collapseEngineErrorSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// redactEngineError 掩盖错误 detail 中凭据形态的值。它
// 一行，且不能引入假的 Markdown 块。redactEngineError 掩盖错误 detail 中凭据形态的值。它刻意镜像 TUI 的策略而不导入 internal/llm。clipErrorRunes 把 s 限制在最多 max 个 rune，用 "…" 标记截断。它按 rune 工作，因此 CJK 字符或 emoji 从不被切掉一半。
func redactEngineError(s string) string {
	s = engineCredentialFieldRE.ReplaceAllString(s, "${1}${2}[redacted]")
	return engineKnownSecretRE.ReplaceAllString(s, "[redacted]")
}

// clipErrorRunes 把 s 限制在最多 max 个 rune，用 "…" 标记截断。它
// 按 rune 工作，因此 CJK 字符或 emoji 从不被切掉一半。
func clipErrorRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

// engineErrorNotice 把 engine 错误格式化为 transcript 的一条有界、单行、脱敏通知。nil 或空错误仍产生可见横幅，使失败的一轮永不看起来像静默空回复。
// 单行、脱敏通知。nil 或空错误仍产生可见
// 横幅，使失败的一轮永不看起来像静默空回复。
func engineErrorNotice(err error) string {
	prefix := tr(engineErrorPrefix)
	if err == nil {
		return strings.TrimSpace(prefix)
	}
	detail := clipErrorRunes(redactEngineError(collapseEngineErrorSpace(err.Error())), maxEngineErrorDetail)
	if detail == "" {
		return strings.TrimSpace(prefix)
	}
	return prefix + detail
}

// finishEngineError 是失败的 engine 提示的唯一集成点。
// main.go 的 completeFromPrompt 可以把它内联的调用
//
//	app.finish("⚠ 引擎错误: " + err.Error())
//	app.status = "引擎错误"
//
// 替换为 app.finishEngineError(err)。取消由调用方处理（见
// finishEngineError 是失败的 engine 提示的唯一集成点。main.go 的 completeFromPrompt 可以把它内联的调用替换为 app.finishEngineError(err)。取消由调用方处理（见 isCancelError）且绝不能到达这里。finish 本身在 main.go 中定义；把调用保留在这里意味着无需更改 engine 或 bridge 代码。
// 把调用保留在这里意味着无需更改 engine 或 bridge 代码。
func (app *chat) finishEngineError(err error) {
	app.finish(engineErrorNotice(err))
	app.status = tr("引擎错误")
}
