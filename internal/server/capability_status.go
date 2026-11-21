package server

import (
	"bytes"
	"encoding/json"
	"sort"
	"sync"
)

// 本文件提供一个带锁的能力目录（capability board），用于汇聚工具能力的开关状态，
// 例如 apply_patch / grep_ex 这类 opt-in 工具。它是纯内存的：不注册路由、不读写
// 磁盘、不读全局状态、不联系任何服务，默认零调用方。未来可由 /v1/status 或诊断
// 入口汇聚展示；本改动只提供原语，不接线。

// Capability 描述单个工具能力的状态。
type Capability struct {
	Name     string `json:"name"`
	Enabled  bool   `json:"enabled"`
	Category string `json:"category"`
}

// capabilityView 是 JSON 序列化的投影。字段按字典序排列（category/enabled），
// 保证每个值对象的键按字母序输出；能力名称作为外层映射的键出现，故不在此重复。
type capabilityView struct {
	Category string `json:"category"`
	Enabled  bool   `json:"enabled"`
}

// CapabilityBoard 按名称保存能力状态。所有方法都可安全并发调用。
//
// 零值即可用：capabilities 延迟初始化，因此 var b CapabilityBoard 与
// &CapabilityBoard{} 行为一致，默认空（无任何能力）。
type CapabilityBoard struct {
	mu           sync.RWMutex
	capabilities map[string]Capability
}

// Register 注册（或覆盖）一项能力。同名能力会被最新一次 Register 的完整状态覆盖。
// 空名称是允许的，与其它名称一样按字典序参与排序与序列化。
func (b *CapabilityBoard) Register(name, category string, enabled bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.capabilities == nil {
		b.capabilities = make(map[string]Capability)
	}
	b.capabilities[name] = Capability{Name: name, Category: category, Enabled: enabled}
}

// Set 切换已注册能力的开关状态。名称不存在时为空操作（不会凭空创建能力）；
// 零值 board 上调用也是安全的空操作。
func (b *CapabilityBoard) Set(name string, enabled bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.capabilities == nil {
		return
	}
	cap, ok := b.capabilities[name]
	if !ok {
		return
	}
	cap.Enabled = enabled
	b.capabilities[name] = cap
}

// Snapshot 返回当前所有能力的副本，按 Name 字典序排列。返回值是新建切片，
// 其中的 Capability 均为值拷贝，调用方修改返回值不影响 board。零值返回非 nil 空切片。
func (b *CapabilityBoard) Snapshot() []Capability {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]Capability, 0, len(b.capabilities))
	for _, cap := range b.capabilities {
		out = append(out, cap)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// JSON 把能力目录序列化为稳定、紧凑的 JSON 字节。稳定性体现在：
//
//   - 键排序：外层以能力名称为键，由 encoding/json 按字典序输出；每个值对象的
//     键（category/enabled）也按字典序排列，输出与插入顺序无关；
//   - CJK 安全：SetEscapeHTML(false) 让中文与 <、>、& 按 UTF-8 原样输出，
//     不产生 \uXXXX 转义；
//   - 去尾换行：json.Encoder 追加的末尾 \n 被去掉。
//
// 零值（无任何能力）输出 {}。该方法不修改接收者，同一状态总是返回逐字节一致的切片。
func (b *CapabilityBoard) JSON() []byte {
	snapshot := b.Snapshot()

	// 用名称作键构造映射，让 encoding/json 对键排序；重复名称在 board 内已被去重。
	views := make(map[string]capabilityView, len(snapshot))
	for _, cap := range snapshot {
		views[cap.Name] = capabilityView{Category: cap.Category, Enabled: cap.Enabled}
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	// views 仅由 string 键与 capabilityView 组成，Encode 不会失败。
	_ = enc.Encode(views)
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}
