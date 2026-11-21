package app

import (
	"bytes"
	"encoding/json"
	"sort"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// 本文件提供一个纯函数的状态快照聚合器。它把一轮状态、就绪门控与配置完整性三个
// 分段聚合成一份可序列化的快照，供诊断入口（如 server.StatusProvider）按需消费。
// 它默认零调用方：不注册路由、不写磁盘、不读全局状态、不联系任何服务。

// StatusSection 是一个状态分段的只读键值投影。键和值都是字符串，因此所有内容
// 都能在序列化前统一脱敏；结构化字段由调用方在传入前自行折叠成键值对。
type StatusSection struct {
	Values map[string]string `json:"values,omitempty"`
}

// StatusSnapshot 把三个可选分段聚合成一份快照。三个字段都是指针且都带
// omitempty，因此零值 StatusSnapshot{} 序列化为 {}；新增分段也不会改变旧输出的
// 字节序列。
type StatusSnapshot struct {
	Turn      *StatusSection `json:"turn,omitempty"`
	Readiness *StatusSection `json:"readiness,omitempty"`
	Integrity *StatusSection `json:"integrity,omitempty"`
}

// NewStatusSnapshot 从三个键值映射构建快照。
//
// 它是纯函数：逐个复制映射，调用方后续修改入参不会影响快照。nil 或空映射对应的
// 分段为 nil，从而被 omitempty 省略；三个都为空时 JSON() 输出 {}。重复键按调用方
// 传入映射的迭代结果决定，但 JSON() 会在序列化前排序键，因此输出仍然逐字节稳定。
func NewStatusSnapshot(turn, readiness, integrity map[string]string) StatusSnapshot {
	return StatusSnapshot{
		Turn:      newStatusSection(turn),
		Readiness: newStatusSection(readiness),
		Integrity: newStatusSection(integrity),
	}
}

// newStatusSection 复制映射；空映射返回 nil 使该分段被省略。
func newStatusSection(values map[string]string) *StatusSection {
	if len(values) == 0 {
		return nil
	}
	copied := make(map[string]string, len(values))
	for key, value := range values {
		copied[key] = value
	}
	return &StatusSection{Values: copied}
}

// JSON 把快照序列化为稳定、脱敏的 JSON 字节。稳定性体现在：
//
//   - 脱敏：每个分段的键与值都先经 security.Redact，具有凭据形状的片段被替换为
//     占位符，Unicode Tag 字符被剥离；
//   - 排序稳定：键先按字典序排序再脱敏写入，因此即使两个键脱敏后碰撞成同一个键，
//     胜出者也由固定顺序决定，输出与映射的插入顺序无关；结构体字段顺序固定；
//   - CJK 安全：SetEscapeHTML(false) 让中文与 <、>、& 按 UTF-8 原样输出，
//     不产生 \uXXXX 转义；
//   - 去尾换行：json.Encoder 追加的末尾 \n 被去掉。
//
// 该方法不修改接收者，同一快照总是返回逐字节一致的切片。
func (s StatusSnapshot) JSON() []byte {
	sanitized := StatusSnapshot{
		Turn:      sanitizeStatusSection(s.Turn),
		Readiness: sanitizeStatusSection(s.Readiness),
		Integrity: sanitizeStatusSection(s.Integrity),
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	// StatusSnapshot 全由基本类型、结构体与 map[string]string 组成，Encode 不会失败。
	_ = enc.Encode(sanitized)
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

// sanitizeStatusSection 对单个分段做"排序键 -> 脱敏 -> 写入"。nil 分段保持 nil，
// 使 omitempty 继续生效。
func sanitizeStatusSection(section *StatusSection) *StatusSection {
	if section == nil || len(section.Values) == 0 {
		return nil
	}
	names := make([]string, 0, len(section.Values))
	for name := range section.Values {
		names = append(names, name)
	}
	sort.Strings(names)

	safe := make(map[string]string, len(names))
	for _, name := range names {
		safe[security.Redact(name)] = security.Redact(section.Values[name])
	}
	return &StatusSection{Values: safe}
}
