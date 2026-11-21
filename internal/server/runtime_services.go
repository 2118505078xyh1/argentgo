package server

import (
	"sort"
	"strconv"
	"strings"
)

// 本文件提供运行时服务拓扑的“自描述”生成器：把监听地址、端口和可用性
// 集合渲染成一段可直接注入 prompt 的结构化文本，让模型知道运行时暴露了
// 哪些服务、在哪个端点可达、当前是否可用。
//
// 设计借鉴 OpenHands 的 RUNTIME_SERVICES 思想：把“运行时能力”作为一等公民
// 写进系统上下文，而不是让模型靠猜。与 OpenHands 一样，这里只产出描述文本，
// 不触发任何网络连接，也不改变现有 HTTP / SSE / RPC / ACP 服务的任何行为。
//
// 这里刻意只交付纯函数：默认没有任何调用方，行为完全不变。接线点见文件末尾
// 注释。要启用时由调用方（例如 internal/app）把产物拼进系统 prompt。

const (
	runtimeServicesOpenTag  = "<RUNTIME_SERVICES>"
	runtimeServicesCloseTag = "</RUNTIME_SERVICES>"
	// runtimeServicesServiceTag 是单个服务条目的标签名。
	runtimeServicesServiceTag = "service"
)

// RuntimeService 描述运行时暴露的一项服务。
//
// Name      服务标识，例如 "http"、"rpc"、"acp"。
// Address   监听地址，例如 "127.0.0.1"、"0.0.0.0"、"[::1]" 或 "::1"。
// Port      监听端口；[1, 65535] 之外的取值一律视为“未分配”。
// Available 服务当前是否可用；与端点是否可达无关，仅如实透传。
type RuntimeService struct {
	Name      string
	Address   string
	Port      int
	Available bool
}

// runtimeServicesPrompt 把服务集合渲染为可注入 prompt 的结构化文本。
//
// 输出形如：
//
//	<RUNTIME_SERVICES>
//	<service name="http" address="0.0.0.0" port="8080" endpoint="http://0.0.0.0:8080" available="true" />
//	</RUNTIME_SERVICES>
//
// 性质：
//   - 纯函数：不访问网络、不读全局状态，相同输入永远得到逐字节相同的输出；
//   - 确定顺序：先按 Name、再按 Address、最后按 Port 字典序排序，调用方无需
//     关心传入切片顺序；
//   - 不改入参：排序在副本上进行，传入切片不被重排；
//   - 字段稳定：每个条目都带 name/address/port/endpoint/available，CJK 字符
//     按 UTF-8 原样保留，经 strconv.Quote 转义后不会破坏标签结构；
//   - 空集合：仍输出成对的空容器，便于调用方无分支地拼接。
func runtimeServicesPrompt(services []RuntimeService) string {
	ordered := append([]RuntimeService(nil), services...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Name != ordered[j].Name {
			return ordered[i].Name < ordered[j].Name
		}
		if ordered[i].Address != ordered[j].Address {
			return ordered[i].Address < ordered[j].Address
		}
		return ordered[i].Port < ordered[j].Port
	})

	var b strings.Builder
	b.WriteString(runtimeServicesOpenTag)
	b.WriteByte('\n')
	for _, svc := range ordered {
		port := normalizedPort(svc.Port)
		b.WriteByte('<')
		b.WriteString(runtimeServicesServiceTag)
		b.WriteString(" name=")
		b.WriteString(quoteAttr(svc.Name))
		b.WriteString(" address=")
		b.WriteString(quoteAttr(svc.Address))
		b.WriteString(" port=")
		b.WriteString(strconv.Quote(strconv.Itoa(port)))
		b.WriteString(" endpoint=")
		b.WriteString(quoteAttr(formatEndpoint(svc.Address, port)))
		b.WriteString(" available=")
		b.WriteString(strconv.Quote(strconv.FormatBool(svc.Available)))
		b.WriteString(" />\n")
	}
	b.WriteString(runtimeServicesCloseTag)
	return b.String()
}

// normalizedPort 把端口收敛到 [1, 65535]：合法值原样返回，其余（含 0、
// 负数与超范围值）统一记作 0，表示“未分配”。这样无论传入什么越界值，
// 生成的文本都稳定且不会描述一个不可达的端口。
func normalizedPort(port int) int {
	if port < 1 || port > 65535 {
		return 0
	}
	return port
}

// formatEndpoint 根据监听地址与端口生成可访问的 http 端点。
//
// 规则：
//   - 含冒号且未加方括号的地址按 IPv6 字面量包裹方括号（"::1" → "[::1]"）；
//     已带方括号的地址不重复包裹；
//   - 端口为 0（未分配）时端点只保留主机部分；
//   - 端点方案固定为 http；本函数不探测协议或连通性，仅做字符串拼装。
func formatEndpoint(address string, port int) string {
	host := strings.TrimSpace(address)
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	endpoint := "http://" + host
	if port == 0 {
		return endpoint
	}
	return endpoint + ":" + strconv.Itoa(port)
}

// quoteAttr 生成可安全放进结构化文本引号内的属性值。先用实体替换掉
// & < > 防止内容伪装成新标签或属性，再交给 strconv.Quote 处理引号、
// 反斜杠与控制字符；CJK 等可打印 Unicode 原样保留，不产生 \uXXXX 转义。
func quoteAttr(s string) string {
	s = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
	return strconv.Quote(s)
}

// 接线点（未实施，默认关闭）：
//
// 本生成器不挂钩到任何现有入口，HTTP / SSE / RPC / ACP 的响应与提示文本
// 保持逐字节不变。若将来要让模型感知运行时拓扑，建议：
//
//  1. 在构造系统 prompt 的装配处（internal/app 或 internal/agent）注入一段
//     runtimeServicesPrompt(...) 的产物；
//  2. 服务清单由监听配置与各传输的启用状态汇总而来，Address/Port 与真实
//     绑定保持一致，Available 由 health 探针填充；
//  3. 通过开关控制是否注入，默认关闭，以保证现有 prompt 快照与测试不变。
//
// 在没有明确的调用方与事件源之前不实施上述步骤，避免为一种尚无生产者的
// 上下文改动已冻结的提示词与线协议。
