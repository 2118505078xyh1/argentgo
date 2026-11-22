package pluginapi

import (
	"errors"
	"fmt"
	"sync"
)

// txn.go 在静态 Registry 之上补一层事务性注册与失效句柄。
//
// 背景：动态加载（会话级扩展、插件热重载）需要"先收集全部注册，成功才一次性
// 生效；中途失败则不留任何痕迹"。Registry 本身只提供单个 Register，重名即报错，
// 但一旦批量注册到一半失败，就无法回滚已写入的条目。这里通过同包访问 Registry
// 的私有字段，在持锁状态下预检全部名字后一次性写入，得到真正的原子提交。
//
// 失效句柄解决另一个问题：跨会话的异步回调若持有旧注册表的快照，会话切换后
// 仍可能把结果写进新会话。Handle 绑定提交时的 generation，注册表一旦再提交，
// 旧句柄立即失效并显式报错，而不是静默作用到新上下文。

// ErrStaleHandle 表示句柄所属的注册表自句柄创建后已经提交过新版本。
var ErrStaleHandle = errors.New("pluginapi: stale registration handle")

// pendingItem 是一条尚未提交的注册项。
type pendingItem[T any] struct {
	name    string
	order   int
	factory func() T
}

// TxRegistry 是支持事务提交的注册表包装。零值不可用，须用 NewTxRegistry 创建。
type TxRegistry[T any] struct {
	mu         sync.Mutex
	target     *Registry[T]
	generation uint64
}

// NewTxRegistry 创建绑定到 target 的事务注册表。
func NewTxRegistry[T any](target *Registry[T]) *TxRegistry[T] {
	return &TxRegistry[T]{target: target}
}

// Tx 是一次事务。通过 Begin 获取；同一事务只能 Commit 或 Discard 一次。
type Tx[T any] struct {
	owner   *TxRegistry[T]
	pending []pendingItem[T]
	done    bool
}

// Begin 开启一个空事务。
func (r *TxRegistry[T]) Begin() *Tx[T] {
	return &Tx[T]{owner: r}
}

// Register 把一条注册加入事务的待提交列表，不触碰目标注册表。
// 事务内部重名立即报错，避免等到提交才发现自相矛盾。
func (tx *Tx[T]) Register(name string, order int, factory func() T) error {
	if tx.done {
		return fmt.Errorf("pluginapi: transaction already finished")
	}
	if name == "" {
		return fmt.Errorf("pluginapi: empty registration name")
	}
	if factory == nil {
		return fmt.Errorf("pluginapi: nil factory for %q", name)
	}
	for _, item := range tx.pending {
		if item.name == name {
			return fmt.Errorf("pluginapi: duplicate registration %q in transaction", name)
		}
	}
	tx.pending = append(tx.pending, pendingItem[T]{name: name, order: order, factory: factory})
	return nil
}

// Commit 原子地把全部待提交项写入目标注册表。
//
// 在目标注册表锁内先预检所有名字是否与已有条目冲突：只要有一个冲突，整批
// 都不写入，generation 也不变。全部通过后才一次性写入并递增 generation，
// 使并发读者只会看到提交前或提交后的完整注册表。
func (tx *Tx[T]) Commit() error {
	if tx.done {
		return fmt.Errorf("pluginapi: transaction already finished")
	}
	owner := tx.owner
	owner.mu.Lock()
	defer owner.mu.Unlock()

	target := owner.target
	target.mu.Lock()
	defer target.mu.Unlock()

	if target.items == nil {
		target.items = make(map[string]registration[T])
	}
	for _, item := range tx.pending {
		if _, exists := target.items[item.name]; exists {
			return fmt.Errorf("pluginapi: duplicate registration %q", item.name)
		}
	}
	for _, item := range tx.pending {
		target.items[item.name] = registration[T]{
			name:    item.name,
			order:   item.order,
			factory: item.factory,
		}
	}
	owner.generation++
	tx.done = true
	tx.pending = nil
	return nil
}

// Discard 丢弃事务中的待提交项。对已提交的事务是空操作。
func (tx *Tx[T]) Discard() {
	if tx.done {
		return
	}
	tx.done = true
	tx.pending = nil
}

// Generation 返回目标注册表成功提交的次数，用于诊断与句柄失效判断。
func (r *TxRegistry[T]) Generation() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.generation
}

// Handle 是绑定到创建时 generation 的注册表句柄。
type Handle[T any] struct {
	owner      *TxRegistry[T]
	generation uint64
}

// Handle 返回一个绑定当前 generation 的句柄。
func (r *TxRegistry[T]) Handle() *Handle[T] {
	r.mu.Lock()
	defer r.mu.Unlock()
	return &Handle[T]{owner: r, generation: r.generation}
}

// Valid 报告句柄是否仍对应注册表的最新版本。
func (h *Handle[T]) Valid() bool {
	if h == nil {
		return false
	}
	return h.owner.Generation() == h.generation
}

// Factories 在句柄有效时返回目标注册表的构造器快照；句柄失效时返回
// ErrStaleHandle，让调用方显式停止使用旧会话的注册结果。
func (h *Handle[T]) Factories() ([]func() T, error) {
	if !h.Valid() {
		return nil, ErrStaleHandle
	}
	return h.owner.target.Factories(), nil
}
