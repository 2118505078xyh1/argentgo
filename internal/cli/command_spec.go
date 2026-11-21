package cli

import (
	"errors"
	"fmt"
	"sort"
	"sync"
)

// ErrDuplicateCommand 表示注册的命令名或别名与已注册项冲突（也覆盖同一条规格
// 内部的重名）。调用方可用 errors.Is 匹配该哨兵错误，并从错误信息中读出冲突
// 的具体名称。
var ErrDuplicateCommand = errors.New("cli: duplicate command name or alias")

// CommandSpec 是一条注册式命令的静态描述：名称、别名、排序权重与一句话摘要。
// 它只承载数据，不含任何调用逻辑——运行时的分发仍由现有子命令表负责，本类型
// 仅为"编译期静态注册"提供原语。当前尚未接线到分发路径（见接线点报告），因此
// 不改变任何现有命令的字节级输出。
type CommandSpec struct {
	Name    string
	Aliases []string
	Order   int
	Summary string
}

// CommandRegistry 收集 CommandSpec，提供重名检测、按名称（含别名）查找与按
// (Order, Name) 稳定排序的列表。
//
// 零值即可用：内部映射在首次 Register 时惰性初始化，只读方法的零值行为为
// "查不到、列表为空"。内部用 RWMutex 保护并发的注册与读取。
type CommandRegistry struct {
	mu    sync.RWMutex
	specs []CommandSpec
	byKey map[string]CommandSpec // 主名与别名都指向同一条规格
}

// cloneSpec 返回规格的深拷贝，连同 Aliases 的底层数组一起复制，使注册表与
// 调用方互不持有对方的可变切片。
func cloneSpec(c CommandSpec) CommandSpec {
	out := c
	if c.Aliases != nil {
		out.Aliases = append([]string(nil), c.Aliases...)
	}
	return out
}

// Register 把一条命令规格加入注册表。主名与所有别名都作为查找键；任一键与
// 已注册项冲突，或同一条规格内部出现重复键（例如别名等于主名），都返回包装了
// ErrDuplicateCommand 的错误，且注册表保持原样不变。
//
// 注册时对 Aliases 做防御性拷贝，调用方后续修改传入切片不会影响已注册内容。
func (r *CommandRegistry) Register(c CommandSpec) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.byKey == nil {
		r.byKey = make(map[string]CommandSpec)
	}

	c = cloneSpec(c)
	keys := make([]string, 0, 1+len(c.Aliases))
	keys = append(keys, c.Name)
	keys = append(keys, c.Aliases...)

	// 先完整校验再提交，保证冲突时注册表不被部分修改。
	pending := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if _, exists := r.byKey[key]; exists {
			return fmt.Errorf("%w: %q", ErrDuplicateCommand, key)
		}
		if _, exists := pending[key]; exists {
			return fmt.Errorf("%w: %q", ErrDuplicateCommand, key)
		}
		pending[key] = struct{}{}
	}
	for _, key := range keys {
		r.byKey[key] = c
	}
	r.specs = append(r.specs, c)
	return nil
}

// Lookup 按名称查找命令，主名与别名等价。未注册（含零值注册表）时返回零值
// 规格与 false。返回的规格是内部内容的副本，修改它不会影响注册表。
func (r *CommandRegistry) Lookup(name string) (CommandSpec, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.byKey[name]
	if !ok {
		return CommandSpec{}, false
	}
	return cloneSpec(c), true
}

// List 返回全部已注册规格的深拷贝，按 (Order, Name) 升序排列：先比 Order，
// 相同再按 Name 的字节序。使用稳定排序保证多次调用顺序一致；返回切片与内部
// 存储及其 Aliases 均不共享底层数组，调用方可自由修改。
func (r *CommandRegistry) List() []CommandSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]CommandSpec, len(r.specs))
	for i, spec := range r.specs {
		out[i] = cloneSpec(spec)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		return out[i].Name < out[j].Name
	})
	return out
}
