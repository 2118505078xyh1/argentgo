package app

import (
	"bytes"
	"encoding/json"
	"sort"

	"example.com/argentgo/internal/server"
)

// 本文件把纯函数的 StatusSnapshot（以及可选的能力目录）适配为
// server.StatusProvider。适配器本身不注册路由、不读写磁盘、不读全局状态、
// 不联系任何服务；是否暴露 /v1/status 仍由 server.Config 决定。

// statusProvider 是 StatusSnapshot 与 server.StatusProvider 之间的桥。
//
// 默认（capabilities == nil）时 StatusJSON 逐字节返回快照自身的 JSON：
// 这正是“注入后 /v1/status 返回 StatusSnapshot JSON”的语义，且不会改动
// StatusSnapshot 既有三段（turn/readiness/integrity）的字节形态。
//
// capabilities 非 nil 时额外汇聚 server.CapabilityBoard 的能力目录。它是
// 另一类原语：StatusSnapshot 描述一轮状态/就绪门控/配置完整性，而
// CapabilityBoard 描述工具能力的开关（apply_patch、grep_ex 等）。二者共同
// 构成诊断面的不同分段，故此处以可选的顶层 "capabilities" 字段组合，而非
// 把能力塞进 StatusSnapshot 的三个既有分段——那样会改变其 wire 形态。
type statusProvider struct {
	snapshot     StatusSnapshot
	capabilities *server.CapabilityBoard
}

// newStatusProvider 构造仅含状态快照的提供者，供包内显式构造与测试使用。
// 生产路径走 newStatusProviderFromConfig（由 configInfoProvider 嵌入接入）。
// 三段映射的语义与 NewStatusSnapshot 相同，序列化前统一脱敏。
func newStatusProvider(turn, readiness, integrity map[string]string) server.StatusProvider {
	return &statusProvider{snapshot: NewStatusSnapshot(turn, readiness, integrity)}
}

// newStatusProviderFromConfig 从已解析的 ConfigInfo 构造默认提供者：把
// model/provider 作为 integrity 分段，使 /v1/status 在未显式构造分段时也
// 返回有意义的状态。刻意不纳入 workspace：路径可能泄露用户主目录，而
// StatusSnapshot.JSON 只做凭据脱敏、不做路径折叠。
func newStatusProviderFromConfig(info server.ConfigInfo) statusProvider {
	return statusProvider{snapshot: NewStatusSnapshot(nil, nil, map[string]string{
		"model":    info.Model,
		"provider": info.Provider,
	})}
}

// StatusJSON 实现 server.StatusProvider：返回已编码好的状态字节，
// 不追加换行、不做再编码（server 侧原样写入）。
func (p *statusProvider) StatusJSON() []byte {
	body := p.snapshot.JSON()
	if p.capabilities == nil {
		return body
	}
	return mergeStatusFields(body, "capabilities", p.capabilities.JSON())
}

// newStatusProviderWithCapabilities 构造附带能力目录的状态提供者，供包内与测试使用。
// 它把 capability_status.go 的 CapabilityBoard 汇聚进 /v1/status 的顶层
// "capabilities" 字段，使"未来由 /v1/status 接入"的自述真正落地：
// 能力目录的 JSON（键为能力名，值为 category/enabled）被逐字节嵌入，
// 与其它分段一起按顶层键字典序稳定输出。
func newStatusProviderWithCapabilities(turn, readiness, integrity map[string]string, board *server.CapabilityBoard) server.StatusProvider {
	return &statusProvider{
		snapshot:     NewStatusSnapshot(turn, readiness, integrity),
		capabilities: board,
	}
}

// mergeStatusFields 把一段 JSON 对象字节作为新字段合并进 base。
//
// 稳定性：先解码为 json.RawMessage 映射以保留各分段的原始字节，再按字段名
// 字典序手写编码；键用 json.Marshal 转义，值原样写出。SetEscapeHTML(false)
// 的语义在此延续到新键本身（本模块的键均为 ASCII 固定名），而两个输入
// （StatusSnapshot.JSON 与 CapabilityBoard.JSON）本身已保证 CJK 原样、无尾换行。
// 输入非法或为空时按空对象处理，绝不 panic。
func mergeStatusFields(base []byte, name string, field []byte) []byte {
	fields := make(map[string]json.RawMessage)
	if len(base) > 0 {
		_ = json.Unmarshal(base, &fields)
	}
	fields[name] = json.RawMessage(field)

	names := make([]string, 0, len(fields))
	for key := range fields {
		names = append(names, key)
	}
	sort.Strings(names)

	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, key := range names {
		if i > 0 {
			buf.WriteByte(',')
		}
		encoded, _ := json.Marshal(key)
		buf.Write(encoded)
		buf.WriteByte(':')
		buf.Write(fields[key])
	}
	buf.WriteByte('}')
	return buf.Bytes()
}
