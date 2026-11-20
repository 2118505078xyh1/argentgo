package cli

import (
	"strings"
	"unicode"
)

// cliErrorHintMaxRunes 限制参与关键词匹配的输入长度（按 Unicode 码点计）。
// 错误信息通常很短，这里设一个宽松上限，既避免对异常超长输入做无界扫描，
// 又不会截断任何真实错误信息中可能出现的关键词。
const cliErrorHintMaxRunes = 512

// cliErrorHintRule 描述一个失败分支：keywords 为命中即归入该分支的关键词表
// （全部为小写；中文关键词无大小写之分），hint 为对应的简体中文修复建议。
type cliErrorHintRule struct {
	keywords []string
	hint     string
}

// cliErrorHintRules 按优先级从高到低排列。当一条错误信息同时命中多个分支时，
// 取第一个命中的分支：更具体的环境问题（TLS、文件、终端）优先于通用的凭证问题。
// 关键词匹配有意偏向召回率：宁可多给一条提示，也不漏掉可操作的修复建议。
var cliErrorHintRules = []cliErrorHintRule{
	{
		// TLS / 证书校验失败。
		keywords: []string{
			"tls", "ssl", "certificate", "x509", "handshake", "证书", "自签名",
		},
		hint: "TLS/证书校验失败：请检查系统时间是否正确、CA 根证书是否齐全，或代理/企业网关是否替换了证书；确认后重试",
	},
	{
		// 文件不存在。
		keywords: []string{
			"no such file", "file not found", "not exist", "does not exist", "enoent", "文件不存在", "找不到文件", "没有该文件", "无法找到文件",
		},
		hint: "文件不存在：请检查路径拼写与当前工作目录，确认文件已创建，必要时改用绝对路径后重试",
	},
	{
		// 非交互环境。
		keywords: []string{
			"non-interactive", "interactive", "not a tty", "no tty", "no terminal", "非交互", "无终端", "没有终端", "无法读取输入",
		},
		hint: "当前为非交互环境：请改用 --yes 等显式标志或命令行参数提供输入，避免在无法读取终端时阻塞",
	},
	{
		// 缺少或无效的凭证（token / API Key / 登录态）。
		keywords: []string{
			"token", "credential", "api key", "apikey", "api_key", "unauthorized", "authentication", "401", "not configured", "凭证", "令牌", "密钥", "未配置", "认证失败",
		},
		hint: "缺少或无效的凭证：请运行 argent login 重新登录，或配置正确的 API Key 后重试",
	},
}

// cliErrorHint 把一条 CLI 错误信息按失败分支归类，返回对应的简体中文修复建议；
// 未命中任何已知分支时返回空串。它是纯函数：只读参数，不产生 I/O、不访问全局状态，
// 返回文案均为固定的中文字符串，长度有界且与输入内容、长度无关，也不会把原文
// 嵌入返回值，因此既能安全地用于表驱动测试，也能在将来由错误输出路径 opt-in
// 调用，而不改变当前任何命令的字节级输出（当前尚未接线，见接线点报告）。
//
// 匹配不区分大小写：输入先按 Unicode 码点做 ToLower 归一化，关键词表均为小写。
// 归一化与截断都按 rune 进行，并在 cliErrorHintMaxRunes 处停止，既不把多字节的
// CJK 字符切断（CJK 安全），也不会对超长输入做无界扫描（有界）。
func cliErrorHint(errMsg string) string {
	if errMsg == "" {
		return ""
	}

	// 按 rune 归一化：大小写折叠 + 有界截断，全程不切分 UTF-8 字符。
	var b strings.Builder
	n := 0
	for _, r := range errMsg {
		if n >= cliErrorHintMaxRunes {
			break
		}
		b.WriteRune(unicode.ToLower(r))
		n++
	}
	lowered := b.String()

	for _, rule := range cliErrorHintRules {
		for _, kw := range rule.keywords {
			if strings.Contains(lowered, kw) {
				return rule.hint
			}
		}
	}
	return ""
}
