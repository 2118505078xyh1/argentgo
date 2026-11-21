package hooks

import "context"

// Middleware 是钩子分发链中的一层。它在 next 前后运行：可以改写 args
// （改写结果会被 base 看到）、决定是否继续调用 next（不调用即短路分发），
// 也可以改写返回的 Result。ctx 与 event 由分发方注入，供 middleware 做
// 上下文相关的判断。
type Middleware func(ctx context.Context, event Event, args *Payload, next func() (Result, error)) (Result, error)

// composeMiddlewares 把 middlewares 从后往前包成洋葱模型：第一个位于最外层，
// 最后一个紧贴 base。返回的调用链为每一层 middleware 套上 guard，使某一层的
// panic 变成携带 ErrCallbackPanic 的错误，且不会越过该层波及外层。
//
// 该函数不执行 I/O、不读写全局状态：ctx、event、args 与 base 全部由调用方注入，
// 所有状态都留在返回的闭包里。middlewares 为空时直接返回 base，不构造任何闭包。
//
// 注意：middleware 的签名需要 ctx/event/args 三个入参，因此组合函数必须一并接收
// 它们，才能在调用 middleware 时原样传入；这是让 middleware 真正拿到分发上下文
// （尤其是可改写的 args 指针）所必需的。
func composeMiddlewares(
	ctx context.Context,
	event Event,
	args *Payload,
	middlewares []Middleware,
	base func() (Result, error),
) func() (Result, error) {
	if len(middlewares) == 0 {
		return base
	}
	call := base
	for i := len(middlewares) - 1; i >= 0; i-- {
		middleware := middlewares[i]
		next := call
		call = func() (Result, error) {
			var result Result
			err := guard(string(event), func() error {
				var runErr error
				result, runErr = middleware(ctx, event, args, next)
				return runErr
			})
			return result, err
		}
	}
	return call
}
