package app

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"example.com/argentgo/internal/llm"
)

// streamIdleTimeoutEnv 是流空闲超时的环境变量名。保持沿用旧名称，
// 使已有的部署/脚本无需改动即可继续生效。
const streamIdleTimeoutEnv = "ARGENT_STREAM_IDLE_TIMEOUT_SECONDS"

// parseStreamIdleTimeout 解析流空闲超时的原始值，返回 (超时时长, 诊断警告文本)。
// 它是纯函数：不读环境、不写状态，便于表驱动测试与诊断通道复用。
//
// 兼容两类写法：
//   - 纯十进制整数（历史格式），按秒解释，例如 "30" → 30s；
//   - Go 时长格式，支持亚秒与小数，例如 "500ms"、"1.5s"。
//
// 未设置（空白）返回 (0, "")：默认禁用且不产生任何诊断输出。
// 非法值或小于等于零的值返回 (0, 非空警告文本)：保持禁用（与旧行为一致），
// 但不再静默——警告文本由调用方经诊断通道输出。
func parseStreamIdleTimeout(raw string) (time.Duration, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, ""
	}
	warning := fmt.Sprintf(
		"警告：环境变量 %s=%q 不是合法的流空闲超时（应为正整数秒数或 Go 时长如 500ms）；已禁用流空闲超时。",
		streamIdleTimeoutEnv, raw)
	// 先兼容历史纯秒数写法：只有整数才按秒解释，"500ms" 等交给 ParseDuration。
	if seconds, err := strconv.Atoi(raw); err == nil {
		if seconds > 0 {
			return time.Duration(seconds) * time.Second, ""
		}
		return 0, warning
	}
	if d, err := time.ParseDuration(raw); err == nil {
		if d > 0 {
			return d, ""
		}
		return 0, warning
	}
	return 0, warning
}

// streamIdleTimeoutFromEnv 从注入的 env 读取并解析流空闲超时，
// 同时返回诊断警告文本。env 为 nil 时回退到进程环境。
func streamIdleTimeoutFromEnv(env func(string) string) (time.Duration, string) {
	if env == nil {
		env = os.Getenv
	}
	return parseStreamIdleTimeout(env(streamIdleTimeoutEnv))
}

// streamIdleTimeout 供 chatter 工厂使用，仅取解析后的时长；
// 诊断警告由 Resolve 统一经诊断通道输出，避免三处工厂重复告警。
func streamIdleTimeout() time.Duration {
	d, _ := streamIdleTimeoutFromEnv(nil)
	return d
}

// chatterFactory 根据 baseURL 与 apiKey 构造某个协议的客户端，并把线协议级选项写入客户端。
type chatterFactory func(base, key string, opts llm.Options) llm.Chatter

// defaultChatterProtocol 是未知协议的回退项在注册表中的键。
const defaultChatterProtocol = "default"

// chatterFactories 是协议名（小写）到构造工厂的注册表。
// 协议通过键查找，因此注册顺序与查找结果无关。
var chatterFactories = make(map[string]chatterFactory)

// registerChatterFactory 把一个协议名绑定到工厂；别名通过多次注册实现。
func registerChatterFactory(protocol string, factory chatterFactory) {
	chatterFactories[protocol] = factory
}

// newAnthropicChatter 构造 Anthropic Messages 客户端，保留原有的默认 max_tokens 设置。
func newAnthropicChatter(base, key string, opts llm.Options) llm.Chatter {
	client := llm.NewAnthropic(base, key)
	client.MaxTokens = llm.DefaultAnthropicMaxTokens
	client.Options = opts
	client.IdleTimeout = streamIdleTimeout()
	client.Retry = retryPolicyFromEnv()
	return client
}

// newResponsesChatter 构造 OpenAI Responses 客户端。
func newResponsesChatter(base, key string, opts llm.Options) llm.Chatter {
	client := llm.NewResponses(base, key)
	client.Options = opts
	client.IdleTimeout = streamIdleTimeout()
	client.Retry = retryPolicyFromEnv()
	return client
}

// newDefaultChatter 构造 OpenAI 兼容的 chat completions 客户端，同时充当未知协议的回退。
func newDefaultChatter(base, key string, opts llm.Options) llm.Chatter {
	client := llm.New(base, key)
	client.Options = opts
	client.IdleTimeout = streamIdleTimeout()
	client.Retry = retryPolicyFromEnv()
	return client
}

func init() {
	// Anthropic Messages 协议及别名。
	registerChatterFactory("anthropic", newAnthropicChatter)
	registerChatterFactory("anthropic-messages", newAnthropicChatter)
	registerChatterFactory("messages", newAnthropicChatter)
	// OpenAI Responses 协议及别名。
	registerChatterFactory("responses", newResponsesChatter)
	registerChatterFactory("openai-responses", newResponsesChatter)
	// OpenAI chat completions 协议，同时作为未知协议的回退。
	registerChatterFactory("default", newDefaultChatter)
	registerChatterFactory("chat", newDefaultChatter)
}
