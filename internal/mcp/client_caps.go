package mcp

import (
	"os"
	"strings"
	"time"
)

// 本文件把 initialize 请求里写死的客户端能力声明和协议号改为可配置的声明层。
// 默认行为保持与历史完全一致：声明空能力对象、使用默认协议号。这里只做声明
// 层的基础设施，不接通任何新的服务器发起请求处理器（roots/sampling/elicitation）。

const (
	// protocolVersionEnv 允许在不重新编译的情况下覆盖 initialize 请求宣告的
	// 协议号。空值或非法值会被忽略并回退到默认值。
	protocolVersionEnv = "ARGENT_MCP_PROTOCOL_VERSION"
)

// clientCapabilityOptions 描述客户端真正实现了哪些可被服务器发起请求的能力。
// 只有存在对应处理器的能力才应置为 true：handleOne 目前会丢弃所有服务器
// 发起的请求，所以默认（零值）不声明任何能力，与历史行为一致。
type clientCapabilityOptions struct {
	// roots 表示已接通 roots/list 处理器。
	roots bool
	// sampling 表示已接通 sampling/createMessage 处理器。
	sampling bool
	// elicitation 表示已接通 elicitation/create 处理器。
	elicitation bool
}

// clientCapabilities 根据已实现的能力生成 initialize 请求里的 capabilities 声明。
// 未实现的能力绝不会被声明，否则合规服务器会向我们发送无法响应的请求；默认
// 零值选项返回空对象，与写死空对象的历史行为逐字节一致。
func clientCapabilities(opts clientCapabilityOptions) map[string]any {
	caps := make(map[string]any)
	if opts.roots {
		caps["roots"] = map[string]any{}
	}
	if opts.sampling {
		caps["sampling"] = map[string]any{}
	}
	if opts.elicitation {
		caps["elicitation"] = map[string]any{}
	}
	return caps
}

// effectiveProtocolVersion 解析本次 initialize 应宣告的协议号。优先使用 Spec
// 上的显式覆盖，其次是环境变量，都没有或值非法时回退到默认协议号。
func effectiveProtocolVersion(spec Spec) string {
	if v := sanitizeProtocolVersion(spec.ProtocolVersion); v != "" {
		return v
	}
	if v := sanitizeProtocolVersion(os.Getenv(protocolVersionEnv)); v != "" {
		return v
	}
	return protocolVersion
}

// sanitizeProtocolVersion 校验一个协议号覆盖值。MCP 协议号采用日期形式
// （如 2024-11-05）；这里用严格解析并比对规范化后的结果，从而拒绝中文、
// 乱填数字、格式错误以及单数字段等值。空或非法值返回空串表示应回退默认值。
func sanitizeProtocolVersion(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	parsed, err := time.Parse("2006-01-02", v)
	if err != nil || parsed.Format("2006-01-02") != v {
		return ""
	}
	return v
}
