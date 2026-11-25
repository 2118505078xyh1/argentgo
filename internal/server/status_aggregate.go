package server

import (
	"bytes"
	"encoding/json"
	"sort"
	"sync"

	"example.com/argentgo/internal/security"
)

// 本文件提供一个带锁的状态快照聚合器。它把若干命名分段（每个分段是一组字符串
// 键值）聚合成一份可序列化、已脱敏的快照。它是纯内存的：不注册路由、不读写磁盘、
// 不读全局状态、不联系任何服务，默认零调用方。

// StatusAggregator 按分段名保存状态快照。所有方法都可安全并发调用。
//
// 零值即可用：sections 延迟初始化，因此 var a StatusAggregator 与
// &StatusAggregator{} 行为一致，默认空（无任何分段）。
type StatusAggregator struct {
	mu       sync.RWMutex
	sections map[string]map[string]string
}

// Set 在 section 名下保存 data 的副本。调用方后续修改 data 不会影响聚合器。
//
// 即使 data 为 nil，也会建立一个空分段（其 JSON 序列化为 "section":{}）。
// 传入空段名是允许的；它与其它段名一样按字典序参与序列化。
func (a *StatusAggregator) Set(section string, data map[string]string) {
	copied := make(map[string]string, len(data))
	for key, value := range data {
		copied[key] = value
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sections == nil {
		a.sections = make(map[string]map[string]string)
	}
	a.sections[section] = copied
}

// Remove 删除 section 分段。段不存在或聚合器为零值时均为空操作。
func (a *StatusAggregator) Remove(section string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sections == nil {
		return
	}
	delete(a.sections, section)
}

// Snapshot 返回当前所有分段的深拷贝：外层映射与每个内层映射都是新建的，
// 调用方修改返回值不会影响聚合器。零值聚合器返回一个非 nil 的空映射，
// 便于调用方直接写入。
func (a *StatusAggregator) Snapshot() map[string]map[string]string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.snapshotLocked()
}

// snapshotLocked 在已持有锁（读锁或写锁）的前提下生成深拷贝。
func (a *StatusAggregator) snapshotLocked() map[string]map[string]string {
	out := make(map[string]map[string]string, len(a.sections))
	for name, values := range a.sections {
		copied := make(map[string]string, len(values))
		for key, value := range values {
			copied[key] = value
		}
		out[name] = copied
	}
	return out
}

// JSON 把聚合器序列化为稳定、脱敏的 JSON 字节。稳定性体现在：
//
//   - 脱敏：分段名、键与值都先经 security.Redact，凭据形状片段被替换为占位符，
//     Unicode Tag 字符被剥离；
//   - 排序稳定：分段名与键都先按字典序排序再脱敏写入，因此即便脱敏后发生键碰撞，
//     胜出者也由固定顺序决定，输出与插入顺序无关；
//   - CJK 安全：SetEscapeHTML(false) 让中文与 <、>、& 按 UTF-8 原样输出，
//     不产生 \uXXXX 转义；
//   - 去尾换行：json.Encoder 追加的末尾 \n 被去掉。
//
// 零分段（含零值聚合器）输出 {}。该方法不修改接收者，同一状态总是返回
// 逐字节一致的切片。
func (a *StatusAggregator) JSON() []byte {
	a.mu.RLock()
	raw := a.snapshotLocked()
	a.mu.RUnlock()

	// 先对分段名排序，使脱敏后碰撞的胜出者确定。
	names := make([]string, 0, len(raw))
	for name := range raw {
		names = append(names, name)
	}
	sort.Strings(names)

	safe := make(map[string]map[string]string, len(names))
	for _, name := range names {
		values := raw[name]
		// 再对内层键排序，同样为了脱敏碰撞时结果确定。
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
		sort.Strings(keys)

		inner := make(map[string]string, len(keys))
		for _, key := range keys {
			inner[security.Redact(key)] = security.Redact(values[key])
		}
		safe[security.Redact(name)] = inner
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	// safe 仅由 map[string]map[string]string 组成，Encode 不会失败。
	_ = enc.Encode(safe)
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}
