package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// Scope 是可逆注册的作用域：把构造期间获取的释放函数集中登记，
// 在 Dispose 时按 LIFO 顺序统一回收。父子关系保证子作用域先于父作用域释放。
//
// 本类型刻意做成零调用方的基础设施：它不接入 Execute，也不改动任何既有 defer
// 或 ctx 链。接线层可按需通过 WithScope 把它附着到 context。
type Scope struct {
	mu         sync.Mutex
	parent     *Scope
	children   []*Scope
	entries    []scopeEntry
	disposed   bool
	disposeErr error // 缓存首次 Dispose 的聚合结果，使幂等调用返回相同错误。
}

// scopeEntry 是一条登记记录；name 仅用于错误信息。
type scopeEntry struct {
	name    string
	release func() error
}

// scopeContextKey 是私有 context 键类型，避免与包外键冲突（仿 batchReceiptEmitterKey 模式）。
type scopeContextKey struct{}

// NewScope 创建一个作用域；parent 为 nil 时创建根作用域。
// 新作用域会挂到 parent 的子列表上，因此 parent.Dispose 会递归回收它。
// parent 已释放时，新作用域直接标记为已释放，避免产生永远无人回收的悬挂子项。
func NewScope(parent *Scope) *Scope {
	s := &Scope{parent: parent}
	if parent == nil {
		return s
	}
	parent.mu.Lock()
	defer parent.mu.Unlock()
	if parent.disposed {
		s.disposed = true
		return s
	}
	parent.children = append(parent.children, s)
	return s
}

// Child 在当前作用域下创建一个子作用域，等价于 NewScope(s)。
// s 为 nil 时创建一个根作用域。
func (s *Scope) Child() *Scope {
	return NewScope(s)
}

// Track 登记一个释放函数。登记顺序入栈，Dispose 时逆序执行。
// release 为 nil 时静默忽略；作用域已释放时无法再登记，此时立即执行该释放函数，
// 避免在回收完成之后泄漏刚刚获取的资源。
func (s *Scope) Track(name string, release func() error) {
	if s == nil || release == nil {
		return
	}
	s.mu.Lock()
	if s.disposed {
		s.mu.Unlock()
		_ = invokeRelease(name, release)
		return
	}
	s.entries = append(s.entries, scopeEntry{name: name, release: release})
	s.mu.Unlock()
}

// Own 运行构造器，并把其返回的释放函数登记到当前作用域。
// 构造失败时回滚当前作用域（按 LIFO 释放此前登记的所有项），返回构造错误；
// 若回滚本身也失败，则用 errors.Join 把两类错误一并返回。
// 构造器在失败时仍返回非 nil 释放函数时，该函数也会加入回滚。
func (s *Scope) Own(build func() (func() error, error)) error {
	if build == nil || s == nil {
		return nil
	}
	release, err := build()
	if err != nil {
		if release != nil {
			s.Track("own.partial", release)
		}
		if derr := s.Dispose(); derr != nil {
			return errors.Join(err, derr)
		}
		return err
	}
	if release != nil {
		s.Track("own", release)
	}
	return nil
}

// Dispose 释放作用域：先递归释放所有子作用域（子先于父），再按 LIFO 释放本作用域
// 登记的项。每个释放函数独立执行：返回的错误用 errors.Join 聚合，panic 转成错误，
// 单个失败不会阻断其余释放。Dispose 幂等，重复调用不再执行释放。
func (s *Scope) Dispose() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.disposed {
		err := s.disposeErr
		s.mu.Unlock()
		return err
	}
	s.disposed = true
	entries := s.entries
	s.entries = nil
	children := s.children
	s.children = nil
	s.mu.Unlock()

	var errs []error
	// 子作用域同样按创建顺序的逆序（LIFO）回收，随后才是本作用域的登记项。
	for i := len(children) - 1; i >= 0; i-- {
		if err := invokeRelease("child", children[i].Dispose); err != nil {
			errs = append(errs, err)
		}
	}
	for i := len(entries) - 1; i >= 0; i-- {
		if err := invokeRelease(entries[i].name, entries[i].release); err != nil {
			errs = append(errs, err)
		}
	}

	result := errors.Join(errs...)
	s.mu.Lock()
	s.disposeErr = result
	s.mu.Unlock()
	return result
}

// invokeRelease 调用单个释放函数：把 panic 转成错误，用 %w 包装释放错误，
// 使调用方可用 errors.Is 匹配底层哨兵错误。
func invokeRelease(name string, fn func() error) (err error) {
	if fn == nil {
		return nil
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("scope release %q panicked: %v", name, r)
		}
	}()
	if e := fn(); e != nil {
		return fmt.Errorf("scope release %q: %w", name, e)
	}
	return nil
}

// WithScope 把作用域附着到 ctx；s 为 nil 时原样返回 ctx。
func WithScope(ctx context.Context, s *Scope) context.Context {
	if s == nil {
		return ctx
	}
	return context.WithValue(ctx, scopeContextKey{}, s)
}

// ScopeOf 从 ctx 取出附着的作用域；未附着或 ctx 为 nil 时返回 nil。
func ScopeOf(ctx context.Context) *Scope {
	if ctx == nil {
		return nil
	}
	s, _ := ctx.Value(scopeContextKey{}).(*Scope)
	return s
}
