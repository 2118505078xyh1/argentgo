package server

import (
	"bytes"
	"encoding/json"
	"sync"
)

// ReliabilityStatusOptInKey 是控制可靠性状态汇聚是否开启的 opt-in 键。
// NewReliabilityStatus 用它向注册表查询开关；主会话在 app 层注册同名 opt-in 键
// 即可把本原语接入 /v1/status。它默认关闭，未显式开启时所有写入都被忽略。
const ReliabilityStatusOptInKey = "reliability_status"

// ReliabilityStatus 把若干命名分段的可靠性状态汇聚到一份可序列化的快照里。
// 它是纯内存的：不注册路由、不读写磁盘、不读全局状态、不联系任何服务，默认关闭。
//
// Enabled 为 false 时 Set 是空操作、Sections 始终为空、JSON 输出 {}。
// 所有方法都可安全并发调用；导出字段由调用方直接改写时不受锁保护，因此构造后
// 不应再被外部改动，也不要拷贝本类型。
type ReliabilityStatus struct {
	mu       sync.RWMutex
	Sections map[string]string
	Enabled  bool
}

// NewReliabilityStatus 用 reg 查询 ReliabilityStatusOptInKey 决定是否开启汇聚。
// 它依赖一个最小接口而非具体注册表，避免 internal/server 反向依赖 internal/app；
// 主会话可传入任何把键转发给 OptInRegistry 的适配器。
// reg 为 nil 或查询结果为 false 时返回关闭的实例（fail-closed）。
func NewReliabilityStatus(reg interface{ Enabled(string) bool }) *ReliabilityStatus {
	s := &ReliabilityStatus{}
	if reg == nil {
		return s
	}
	s.Enabled = reg.Enabled(ReliabilityStatusOptInKey)
	return s
}

// Set 在 section 名下保存 data。实例关闭时为空操作。
func (s *ReliabilityStatus) Set(section, data string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.Enabled {
		return
	}
	if s.Sections == nil {
		s.Sections = make(map[string]string)
	}
	s.Sections[section] = data
}

// JSON 把当前分段序列化为稳定、UTF-8 原样的 JSON 字节。稳定性体现在：
//
//   - 键排序：encoding/json 按字典序输出 map 键，相同状态逐字节一致；
//   - CJK 安全：SetEscapeHTML(false) 让中文与 <、>、& 按 UTF-8 原样输出，
//     不产生 \uXXXX 转义；
//   - 去尾换行：json.Encoder 追加的末尾 \n 被去掉；
//   - 空分段（含零值实例）输出 {} 而非 null。
//
// 该方法不修改接收者；快照在锁内完成拷贝，调用方可安全持有返回的切片。
func (s *ReliabilityStatus) JSON() []byte {
	s.mu.RLock()
	out := make(map[string]string, len(s.Sections))
	for key, value := range s.Sections {
		out[key] = value
	}
	s.mu.RUnlock()

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	// out 仅由 map[string]string 组成，Encode 不会失败。
	_ = enc.Encode(out)
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}
