package provider

import "strings"

// contextWindowRule 是一条「模型名前缀 -> 上下文窗口」的映射规则。
// window 以 token 计，取值来自各供应商公开的模型规格；prefix 为规范化后的小写前缀。
type contextWindowRule struct {
	prefix string
	window int
}

// contextWindowRules 是按「先具体、后宽泛」排列的已知窗口表。
//
// 匹配时对模型名取最后一段（剥离 anthropic/claude-... 这类供应商前缀）、
// 转小写并把 unicode 破折号/下划线/空格统一成 '-'，然后取第一条前缀命中的规则。
// 顺序很关键：gpt-5 必须先于 gpt，否则 256k 型号会被通用 128k 规则吞掉。
//
// 表中全部是经验值，只用于推导回退窗口；用户显式配置的 contextWindow 永远优先。
var contextWindowRules = []contextWindowRule{
	// Anthropic Claude 全系 200k。
	{"claude", 200_000},
	// OpenAI GPT-5 系列 256k；其余 GPT 型号 128k。
	{"gpt-5", 256_000},
	{"gpt", 128_000},
	// Google Gemini 系列 1M。
	{"gemini", 1_000_000},
	// DeepSeek 系列 64k。
	{"deepseek", 64_000},
	// 月之暗面 Kimi / Moonshot 系列 128k。
	{"moonshot", 128_000},
	{"kimi", 128_000},
	// xAI Grok 系列 128k。
	{"grok", 128_000},
	// 智谱 GLM 系列 128k。
	{"glm", 128_000},
	// 通义千问 Qwen 系列 128k。
	{"qwen", 128_000},
	// MiniMax 系列 200k。
	{"minimax", 200_000},
	// R52 研究 #46：本地/聚合供应商的默认模型多为 Llama / Mistral 系。
	// 缺省这些规则会让 ContextWindowFor 返回 0，app 回落到编译默认 32000 而过早压缩丢上下文。
	// Llama 系列 128k；Mistral 系列 128k。置于通用规则之后：前缀不与上述任何规则重叠。
	{"llama", 131_072},
	{"mistral", 128_000},
}

// normalizeWindowModel 把模型名规范化为用于窗口查表的形式：
// 去首尾空白、统一分隔符、只保留最后一段（剥离供应商前缀）、转小写。
// strings 的字节操作对 UTF-8 安全，CJK 字符原样保留，绝不会被误判为 ASCII 前缀。
func normalizeWindowModel(model string) string {
	value := modelSeparators.Replace(strings.TrimSpace(model))
	if i := strings.LastIndex(value, "/"); i >= 0 {
		value = value[i+1:]
	}
	return strings.ToLower(strings.Trim(value, "-"))
}

// KnownContextWindow 按模型名返回已知的上下文窗口 token 数，未知模型返回 0。
//
// 匹配不区分大小写，并自动剥离 "anthropic/claude-sonnet-4.5" 这类供应商前缀；
// 常见 unicode 破折号、下划线与空格按 NormalizeModel 的口径统一处理。
// 这是纯函数：无 I/O、不修改任何入参（字符串不可变），表为有序切片，结果稳定可并发调用。
func KnownContextWindow(model string) int {
	name := normalizeWindowModel(model)
	if name == "" {
		return 0
	}
	for _, rule := range contextWindowRules {
		if strings.HasPrefix(name, rule.prefix) {
			return rule.window
		}
	}
	return 0
}

// ContextWindowFor 按回落链解析应使用的上下文窗口 token 数：
//
//  1. configured > 0 时直接采用用户显式配置；
//  2. 否则按 model 查 KnownContextWindow；
//  3. 再否则用 templateName 解析模板后，按该模板的默认模型查表；
//  4. 都未知时返回 0，让调用方（app）回落到编译默认值 32000。
//
// templateName 经 Resolve 解析，因而接受规范名、别名且大小写不敏感。
// 这是纯函数：不改入参、CJK 安全、结果稳定；不做 I/O，也不读取任何全局可变状态。
func ContextWindowFor(templateName, model string, configured int) int {
	if configured > 0 {
		return configured
	}
	if window := KnownContextWindow(model); window > 0 {
		return window
	}
	if tpl, ok := Resolve(templateName); ok {
		return KnownContextWindow(tpl.DefaultModel)
	}
	return 0
}
