package hooks

import (
	"errors"
	"fmt"
)

// ErrCallbackPanic 是 PanicError 包装的哨兵错误，使调用方能用
// errors.Is 区分 panic 的回调与普通失败。
var ErrCallbackPanic = errors.New("hooks: callback panicked")

// PanicError 是 panic 的回调的结构化结果。CallID 是
// 正在分发的调用的标识；它在 panic 后保留，
// 使运行能报告哪个回调失败而不中止。
type PanicError struct {
	CallID string
	Value  any
}

// Error 实现 error。
func (e *PanicError) Error() string {
	return fmt.Sprintf("hooks: callback for call %q panicked: %v", e.CallID, e.Value)
}

// Unwrap 暴露 ErrCallbackPanic 哨兵。
func (e *PanicError) Unwrap() error { return ErrCallbackPanic }

// guard 运行 fn 并将 panic 转换为携带 callID 的 PanicError。它
// 用于钩子分发边界，使一个行为不端的回调无法中止
// 整个运行：调用方记录错误并继续下一个钩子。
func guard(callID string, fn func() error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = &PanicError{CallID: callID, Value: recovered}
		}
	}()
	return fn()
}
