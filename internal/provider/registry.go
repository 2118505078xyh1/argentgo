package provider

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// 注册相关的哨兵错误，调用方可用 errors.Is 判断。
var (
	// ErrEmptyTemplateName 表示注册项的名称为空。
	ErrEmptyTemplateName = errors.New("provider: empty template name")
	// ErrDuplicateTemplate 表示同名模板被重复注册。
	ErrDuplicateTemplate = errors.New("provider: duplicate template name")
)

// templateRegistration 是一条带显式顺序的模板注册项。
// order 决定 List/Names 的输出顺序，与 pluginapi 的 registration 语义对齐：
// 顺序由调用方显式给出，避免包 init 或切片字面量的排列漂移影响目录与快照。
type templateRegistration struct {
	name     string
	order    int
	template Template
}

// templateRegistry 保存带显式顺序的模板注册表，并发安全，零值可用。
type templateRegistry struct {
	mu    sync.RWMutex
	items map[string]templateRegistration
}

// Register 注册一个命名模板。重名（无论 order 是否相同）返回 ErrDuplicateTemplate，
// 空名称返回 ErrEmptyTemplateName，让配置错误在启动期暴露而非静默覆盖。
func (r *templateRegistry) Register(name string, order int, template Template) error {
	if strings.TrimSpace(name) == "" {
		return ErrEmptyTemplateName
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.items == nil {
		r.items = make(map[string]templateRegistration)
	}
	if _, exists := r.items[name]; exists {
		return fmt.Errorf("%w: %q", ErrDuplicateTemplate, name)
	}
	r.items[name] = templateRegistration{name: name, order: order, template: template}
	return nil
}

// MustRegister 与 Register 相同，但注册失败时 panic。
// 仅供包级静态注册使用——这类失败属于程序员错误，应在进程启动期立即暴露。
func (r *templateRegistry) MustRegister(name string, order int, template Template) {
	if err := r.Register(name, order, template); err != nil {
		panic(err)
	}
}

// ordered 返回按 (order, name) 稳定排序的注册项快照。
// 返回的是新切片，调用方修改其元素不会影响注册表。
func (r *templateRegistry) ordered() []templateRegistration {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.items) == 0 {
		return nil
	}
	list := make([]templateRegistration, 0, len(r.items))
	for _, item := range r.items {
		list = append(list, item)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].order != list[j].order {
			return list[i].order < list[j].order
		}
		return list[i].name < list[j].name
	})
	return list
}

// Templates 返回按 (order, name) 稳定排序的模板快照，修改返回值不影响注册表。
func (r *templateRegistry) Templates() []Template {
	list := r.ordered()
	if len(list) == 0 {
		return nil
	}
	out := make([]Template, 0, len(list))
	for _, item := range list {
		out = append(out, item.template)
	}
	return out
}

// Names 返回按 (order, name) 稳定排序的注册名快照，便于诊断与测试。
func (r *templateRegistry) Names() []string {
	list := r.ordered()
	if len(list) == 0 {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		out = append(out, item.name)
	}
	return out
}

// Lookup 按规范名称返回模板。第二个结果报告该名称是否已注册。
func (r *templateRegistry) Lookup(name string) (Template, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	item, ok := r.items[name]
	return item.template, ok
}

// Len 返回已注册的模板数量。
func (r *templateRegistry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.items)
}
