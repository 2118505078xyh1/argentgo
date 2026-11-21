// Package provider 保存内置 provider 模板：每个供应商在哪里、它讲哪种线上协议，以及默认使用哪个模型。
// 每个供应商在哪里、它讲哪种线上协议，以及默认使用哪个模型。
//
// 模板是数据，镜像 Rust 实现中的 crates/provider/providers.json，但硬编码使 Go 二进制无需 asset 文件。该表只描述端点；凭据由调用方通过 Template.APIKeyEnv / Template.APIKeyEnvs 和 Auth 辅助函数从环境读取。
// 实现，但硬编码使 Go 二进制无需 asset 文件。该表只描述端点；凭据由
// 环境读取，由调用方通过
// Template.APIKeyEnv / Template.APIKeyEnvs 和 Auth 辅助函数。
package provider

import "strings"

// Protocol 命名由 internal/llm 实现的线上协议。
type Protocol string

// 支持的协议。
const (
	// ProtocolAnthropic 是 Anthropic Messages API（llm.AnthropicClient）。
	ProtocolAnthropic Protocol = "anthropic-messages"
	// ProtocolOpenAI 是 OpenAI chat completions（llm.Client）。
	ProtocolOpenAI Protocol = "openai-chat-completions"
	// ProtocolResponses 是 OpenAI Responses API（llm.ResponsesClient）。
	ProtocolResponses Protocol = "openai-responses"
)

// Template 描述一个 provider 端点。
type Template struct {
	// Name 是 Resolve 和命令行使用的规范 id。
	Name string
	// Protocol 是要使用的线上协议。
	Protocol Protocol
	// BaseURL 是 API 根；llm 客户端会追加自己的路径。
	BaseURL string
	// APIKeyEnv 命名保存 key 的旧版单一环境变量。空表示该端点通常无需认证（本地服务器）。
	// 空表示该端点通常无需认证（本地服务器）。
	APIKeyEnv string
	// APIKeyEnvs 按优先级命名额外的凭据来源：
	// APIKeyEnvs 按优先级命名额外的凭据来源：第一个非空值胜出。Auth 为兼容性先尝试 APIKeyEnv，再尝试这些名称。
	// 再尝试这些名称。
	APIKeyEnvs []string
	// NoAuth 完全跳过凭据查找且不发送认证请求头。
	// NoAuth 完全跳过凭据查找且不发送认证请求头。它建模上游 provider 选项中的 apiKey:false。
	NoAuth bool
	// DefaultModel 在用户未指定模型时使用。
	DefaultModel string
	// CacheSupport 报告该 provider 至少在一个协议上支持提示缓存。它不强制线上标记：llm 仍只在具有显式稳定前缀表示的协议上发出它们，且本地/兼容端点默认无缓存。
	// 至少在一个协议上支持提示缓存。它不强制线上标记：llm 仍只在
	// 具有显式稳定前缀表示的协议上发出它们，且
	// 本地/兼容端点默认无缓存。
	CacheSupport bool
}

// builtInTemplate 是内置表的一项。order 显式给出，按展示顺序递增（步长 10），
// 便于将来在中间插入模板；它与 name 一起决定稳定排序的结果。
type builtInTemplate struct {
	order    int
	template Template
}

// builtInTemplates 是内置表，字段取值与原切片逐字节一致；order 按原展示顺序分配，
// 保证经 templateRegistry 稳定排序后 List() 的顺序与内容都不变。
var builtInTemplates = []builtInTemplate{
	{10, Template{
		Name:         "anthropic",
		Protocol:     ProtocolAnthropic,
		BaseURL:      "https://api.anthropic.com/v1",
		APIKeyEnv:    "ANTHROPIC_API_KEY",
		DefaultModel: "claude-sonnet-5-5",
		CacheSupport: true,
	}},
	{20, Template{
		Name:         "openai",
		Protocol:     ProtocolOpenAI,
		BaseURL:      "https://api.openai.com/v1",
		APIKeyEnv:    "OPENAI_API_KEY",
		DefaultModel: "gpt-5.6",
		CacheSupport: true,
	}},
	{30, Template{
		Name:         "deepseek",
		Protocol:     ProtocolAnthropic,
		BaseURL:      "https://api.deepseek.com/anthropic",
		APIKeyEnv:    "DEEPSEEK_API_KEY",
		DefaultModel: "deepseek-v4-pro",
	}},
	{40, Template{
		Name:         "moonshot",
		Protocol:     ProtocolAnthropic,
		BaseURL:      "https://api.moonshot.cn/anthropic",
		APIKeyEnv:    "MOONSHOT_API_KEY",
		DefaultModel: "kimi-k3.1-pro",
	}},
	{50, Template{
		Name:         "zhipu",
		Protocol:     ProtocolAnthropic,
		BaseURL:      "https://open.bigmodel.cn/api/anthropic",
		APIKeyEnv:    "ZHIPU_API_KEY",
		DefaultModel: "GLM-5.3",
	}},
	{60, Template{
		Name:         "qwen",
		Protocol:     ProtocolOpenAI,
		BaseURL:      "https://dashscope-intl.aliyuncs.com/compatible-mode/v1",
		APIKeyEnv:    "DASHSCOPE_API_KEY",
		DefaultModel: "qwen3.8-max",
	}},
	{70, Template{
		Name:         "openrouter",
		Protocol:     ProtocolOpenAI,
		BaseURL:      "https://openrouter.ai/api/v1",
		APIKeyEnv:    "OPENROUTER_API_KEY",
		DefaultModel: "anthropic/claude-sonnet-4.5",
	}},
	{80, Template{
		Name:         "ollama",
		Protocol:     ProtocolOpenAI,
		BaseURL:      "http://127.0.0.1:11434/v1",
		DefaultModel: "llama3.2",
	}},
	{90, Template{
		Name:         "lmstudio",
		Protocol:     ProtocolOpenAI,
		BaseURL:      "http://127.0.0.1:1234/v1",
		DefaultModel: "local-model",
	}},
	{100, Template{
		Name:         "xai",
		Protocol:     ProtocolResponses,
		BaseURL:      "https://api.x.ai/v1",
		APIKeyEnv:    "XAI_API_KEY",
		DefaultModel: "grok-4.7",
		CacheSupport: true,
	}},
	{110, Template{
		Name:         "zai",
		Protocol:     ProtocolAnthropic,
		BaseURL:      "https://api.z.ai/api/anthropic",
		APIKeyEnv:    "ZAI_API_KEY",
		DefaultModel: "GLM-5.3",
	}},
	{120, Template{
		Name:         "bigmodel",
		Protocol:     ProtocolAnthropic,
		BaseURL:      "https://open.bigmodel.cn/api/anthropic",
		APIKeyEnv:    "BIGMODEL_API_KEY",
		DefaultModel: "GLM-5.3",
	}},
	{130, Template{
		Name:         "minimax",
		Protocol:     ProtocolAnthropic,
		BaseURL:      "https://api.minimaxi.com/anthropic",
		APIKeyEnv:    "MINIMAX_API_KEY",
		DefaultModel: "MiniMax-M3",
	}},
	{140, Template{
		Name:         "local",
		Protocol:     ProtocolOpenAI,
		BaseURL:      "http://127.0.0.1:3425/v1",
		DefaultModel: "auto/deepseek-v4.1-flash",
	}},
}

// aliases 把替代拼写映射到规范名称。
var aliases = map[string]string{
	"claude":      "anthropic",
	"kimi":        "moonshot",
	"dashscope":   "qwen",
	"aliyun":      "qwen",
	"z.ai":        "zai",
	"glm":         "zhipu",
	"bigmodel.cn": "zhipu",
	"lm-studio":   "lmstudio",
	"lm_studio":   "lmstudio",
	"open-router": "openrouter",
	"grok":        "xai",
}

// defaultTemplates 是包级静态注册表，由 newDefaultTemplates 用内置表填充。
// 注册只发生在包初始化阶段；完成后注册表只读，因此并发查表安全。
var defaultTemplates = newDefaultTemplates()

func newDefaultTemplates() *templateRegistry {
	r := &templateRegistry{}
	for _, entry := range builtInTemplates {
		r.MustRegister(entry.template.Name, entry.order, entry.template)
	}
	return r
}

// List 按展示顺序返回内置模板。切片是副本，修改它不影响该表。
// 副本，修改它不影响该表。
func List() []Template {
	return defaultTemplates.Templates()
}

// Resolve 按规范名称或别名不区分大小写查找模板。第二个结果报告该名称是否已知。
// 第二个结果报告该名称是否已知。
func Resolve(name string) (Template, bool) {
	key := strings.ToLower(strings.TrimSpace(name))
	if canonical, ok := aliases[key]; ok {
		key = canonical
	}
	return defaultTemplates.Lookup(key)
}

var modelSeparators = strings.NewReplacer(
	"\u2010", "-", "\u2011", "-", "\u2012", "-", "\u2013", "-",
	"\u2014", "-", "\u2212", "-", "_", "-", " ", "-",
)

// NormalizeModel 跨模板规范化模型 id：去掉首尾空白，unicode 破折号/下划线变为 '-'，重复分隔符折叠，内置默认值的大小写变体解析回该默认值的规范拼写。未知 id 保持自己的大小写，
// 去掉首尾空白，unicode 破折号/下划线变为 '-'，重复
// 分隔符折叠，内置默认值的大小写变体解析回
// 该默认值的规范拼写。未知 id 保持自己的大小写，
// 包括 "anthropic/claude-sonnet-4.5" 这样的供应商前缀。
func NormalizeModel(id string) string {
	value := modelSeparators.Replace(strings.TrimSpace(id))
	for strings.Contains(value, "--") {
		value = strings.ReplaceAll(value, "--", "-")
	}
	value = strings.Trim(value, "-")
	if value == "" {
		return ""
	}
	for _, template := range defaultTemplates.Templates() {
		if strings.EqualFold(value, modelSeparators.Replace(template.DefaultModel)) {
			return template.DefaultModel
		}
	}
	return value
}

// Model 返回规范化后的覆盖值，或 override 为空时返回模板的规范默认值。
// app/cli 中的调用方可以使用这个钩子而无需
// Model 返回规范化后的覆盖值，或 override 为空时返回模板的规范默认值。app/cli 中的调用方可以使用这个钩子而无需 provider 包了解 config 或 profiles。
func (t Template) Model(override string) string {
	if strings.TrimSpace(override) == "" {
		return NormalizeModel(t.DefaultModel)
	}
	return NormalizeModel(override)
}
