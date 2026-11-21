// Package pluginapi 提供编译期静态注册的通用扩展注册表。
//
// 功能包在自己的 init() 中把构造器注册进来，composition root（app/main）
// 通过 blank import 决定把哪些功能编进二进制。注册的是构造器（func() 返回值）
// 而非实例：每次运行仍可独立实例化一份，避免全局共享可变状态。
//
// 本包刻意只依赖标准库，不 import 任何 internal 包，因此不会触碰 archguard
// 的依赖规则，也不会引入包循环。注册表本身用泛型对类型无感；少数需要跨包
// 共享的纯类型契约（如 Tool 接口，见 tool.go）也放在这里，供各包以类型别名复用。
package pluginapi

import (
	"fmt"
	"sort"
	"sync"
)

// registration 是一条带显式顺序的注册项。order 由调用方给出，
// 避免 Go 跨包 init() 执行顺序不确定导致注册顺序漂移——顺序对
// 工具目录、提示字节与 golden 快照都是可观测的。
type registration[T any] struct {
	name    string
	order   int
	factory func() T
}

// Registry 是类型安全的构造器注册表，并发安全。
// 零值可用。
type Registry[T any] struct {
	mu    sync.RWMutex
	items map[string]registration[T]
}

// Register 注册一个命名构造器。重名（无论 order 是否相同）都报错，
// 让配置错误在启动期暴露而非静默覆盖。
func (r *Registry[T]) Register(name string, order int, factory func() T) error {
	if name == "" {
		return fmt.Errorf("pluginapi: empty registration name")
	}
	if factory == nil {
		return fmt.Errorf("pluginapi: nil factory for %q", name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.items == nil {
		r.items = make(map[string]registration[T])
	}
	if _, exists := r.items[name]; exists {
		return fmt.Errorf("pluginapi: duplicate registration %q", name)
	}
	r.items[name] = registration[T]{name: name, order: order, factory: factory}
	return nil
}

// MustRegister 与 Register 相同，但注册失败时 panic。
// 仅供 init() 中的编译期静态注册使用——这类失败属于程序员错误，
// 应在进程启动期立即暴露。
func (r *Registry[T]) MustRegister(name string, order int, factory func() T) {
	if err := r.Register(name, order, factory); err != nil {
		panic(err)
	}
}

// Factories 返回按 (order, name) 稳定排序的构造器快照。
// 返回的是新切片，调用方修改不会影响注册表。
func (r *Registry[T]) Factories() []func() T {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.items) == 0 {
		return nil
	}
	list := make([]registration[T], 0, len(r.items))
	for _, item := range r.items {
		list = append(list, item)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].order != list[j].order {
			return list[i].order < list[j].order
		}
		return list[i].name < list[j].name
	})
	out := make([]func() T, 0, len(list))
	for _, item := range list {
		out = append(out, item.factory)
	}
	return out
}

// Names 返回按 (order, name) 稳定排序的注册名快照，便于诊断与测试。
func (r *Registry[T]) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.items) == 0 {
		return nil
	}
	list := make([]registration[T], 0, len(r.items))
	for _, item := range r.items {
		list = append(list, item)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].order != list[j].order {
			return list[i].order < list[j].order
		}
		return list[i].name < list[j].name
	})
	out := make([]string, 0, len(list))
	for _, item := range list {
		out = append(out, item.name)
	}
	return out
}

// Len 返回已注册条目数。
func (r *Registry[T]) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.items)
}
