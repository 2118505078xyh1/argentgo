package tools

import "example.com/argentgo/internal/bash"

// RegisterExecSession 把 write_stdin 追加到 r 的末尾（幂等）。
// 本函数绝不被 New/NewWithLimit 调用，因此默认 Specs()/Names() 与 golden 逐字节不变；
// 只有 app 层在 ARGENT_EXEC_SESSION=1 时才调用（TODO #120）。
func RegisterExecSession(r *Registry) {
	if r == nil {
		return
	}
	const name = "write_stdin"
	if _, exists := r.tools[name]; exists {
		return
	}
	r.tools[name] = bash.NewWriteStdinTool()
	r.order = append(r.order, name)
}
