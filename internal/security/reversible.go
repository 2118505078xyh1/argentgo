package security

import "sync"

// 本文件提供"可逆注册 effect"的通用容器：每次 Register 都配发一个幂等的
// release 闭包，调用它即可撤销自己那一次注册。把若干 release 收集起来，在
// scope 销毁时逐个调用，就能自动回滚该 scope 内产生的全部注册 effect
// （借鉴 dsh 的"可逆注册"思想），无需手工记住"注册了什么、该删谁"。
//
// 关键语义：每次 Register 分配一个全局递增的唯一 id，注册记录以 id 为键
// 存放；name 只是索引，指向该名字下仍存活的 id 列表（按注册顺序）。release
// 只按 id 撤销自己那一次注册，绝不按 name 直接删除——因此同名被注册多次时，
// 早先那次的 release 不会误删后注册的记录，反之亦然；撤销最新一次注册后，
// 该名字下更早的存活注册会重新成为当前值。
//
// 本类型只依赖标准库 sync，零值可用，并发安全；不访问任何全局状态、没有
// 调用方（纯 opt-in），对既有"策略 → 审批 → 审计"安全链无任何影响。

// reversibleEntry 是一条注册记录。id 是它自身的唯一身份，name 用于索引，
// value 是注册的值。
type reversibleEntry[T any] struct {
	id    uint64
	name  string
	value T
}

// ReversibleRegistry 是一个泛型的可逆注册表：注册返回 release 闭包，
// scope 销毁时调用 release 即可逐条回滚。零值即可使用，无需显式构造。
type ReversibleRegistry[T any] struct {
	mu      sync.RWMutex
	nextID  uint64                        // 全局递增 id 分配器，仅在 mu 写锁下访问
	entries map[uint64]reversibleEntry[T] // id → 注册记录
	byName  map[string][]uint64           // name → 该名字下仍存活的 id（注册顺序）
}

// Register 注册 name → v，并返回一个幂等的 release 闭包。
//
// 每次调用分配独立的 id，因此允许同名重复注册：Lookup/Snapshot 取该名字下
// 最近注册且仍存活的值。release 只撤销本次注册，重复调用安全且始终返回 nil；
// 返回值类型为 func() error 是为了与通用的 effect 释放接口保持一致。
func (r *ReversibleRegistry[T]) Register(name string, v T) func() error {
	r.mu.Lock()
	if r.entries == nil {
		r.entries = make(map[uint64]reversibleEntry[T])
	}
	if r.byName == nil {
		r.byName = make(map[string][]uint64)
	}
	r.nextID++
	id := r.nextID
	r.entries[id] = reversibleEntry[T]{id: id, name: name, value: v}
	r.byName[name] = append(r.byName[name], id)
	r.mu.Unlock()

	// once 让闭包自身也幂等；releaseByID 内的 id 比对是第二道保障，
	// 即便 once 状态丢失（例如闭包被复制），也不会误删同名的新注册。
	var once sync.Once
	return func() error {
		once.Do(func() { r.releaseByID(id) })
		return nil
	}
}

// releaseByID 撤销 id 对应的那一次注册。若该记录已不存在（已被释放或
// 从未存在），则什么都不做，从而保证 release 的幂等性。
func (r *ReversibleRegistry[T]) releaseByID(id uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	e, ok := r.entries[id]
	if !ok || e.id != id {
		return
	}
	delete(r.entries, id)

	ids := r.byName[e.name]
	for i, candidate := range ids {
		if candidate == id {
			r.byName[e.name] = append(ids[:i], ids[i+1:]...)
			break
		}
	}
	if len(r.byName[e.name]) == 0 {
		delete(r.byName, e.name)
	}
}

// Lookup 返回 name 下当前（最近注册且仍存活）的值。名字不存在或所有注册
// 都已释放时返回零值与 false。
func (r *ReversibleRegistry[T]) Lookup(name string) (T, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	ids := r.byName[name]
	if len(ids) == 0 {
		var zero T
		return zero, false
	}
	e, ok := r.entries[ids[len(ids)-1]]
	if !ok {
		var zero T
		return zero, false
	}
	return e.value, true
}

// Snapshot 返回当前每个名字下最新存活值的副本。修改返回的 map 不影响注册表；
// 注册表后续的变化也不会反映到已返回的副本上。
func (r *ReversibleRegistry[T]) Snapshot() map[string]T {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make(map[string]T, len(r.byName))
	for name, ids := range r.byName {
		if len(ids) == 0 {
			continue
		}
		if e, ok := r.entries[ids[len(ids)-1]]; ok {
			out[name] = e.value
		}
	}
	return out
}

// Len 返回当前仍存活的注册数量（按 id 计）。同名重复注册在各自 release
// 之前分别计数；因此 Len 可能大于 Snapshot 的键数量。
func (r *ReversibleRegistry[T]) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.entries)
}
