// Package shell 的顶层 API：把一行命令解析并执行。
package shell

import (
	"context"
	"time"
)

// Options 是执行选项。依赖通过字段注入，保持包零外部耦合。
type Options struct {
	// Vars 是初始变量表（通常为环境变量）。
	Vars map[string]string
	// Cwd 是工作目录；所有文件操作被限制在此目录内。
	Cwd string
	// Safety 是可选安全咨询谓词；返回 false 拒绝命令。
	Safety func(command string) bool
	// Timeout 是外部命令超时；零值不设超时。
	Timeout time.Duration
	// Ctx 用于取消。
	Ctx context.Context
}

// Result 是一次执行的结果。非零退出通过 ExitStatus 表达，不是 error。
type Result struct {
	Output     string
	ExitStatus int
}

// Run 解析并执行一行命令。
//
// 语义边界（fail-closed）：遇到不支持的构造（循环/函数/case/子 shell/算术/
// 后台任务等）返回 ErrUnsupported，调用方应据此回退或报错，绝不近似解释。
func Run(command string, opts Options) (Result, error) {
	it := &Interp{
		Vars:    copyVars(opts.Vars),
		Cwd:     opts.Cwd,
		Safety:  opts.Safety,
		Timeout: opts.Timeout,
		Ctx:     opts.Ctx,
	}
	if it.Vars == nil {
		it.Vars = map[string]string{}
	}
	tokens, err := newLexer(command).lex()
	if err != nil {
		return Result{ExitStatus: 2}, err
	}
	f, err := parse(tokens)
	if err != nil {
		return Result{ExitStatus: 2}, err
	}
	out, err := it.Run(f)
	return Result{Output: out, ExitStatus: it.LastStatus}, err
}

// Supported 只做解析，不执行：用于探测一条命令是否在本子集的语法范围内。
//
// 判定固定在解析期且递归覆盖整棵 AST（见 fallback.go 的 containsUnsupported）：
// 命令替换体、双引号内的嵌套替换、重定向目标、二元命令两侧等任何一处出现不支持
// 构造，都返回 false。这样调用方能在执行前做"整条命令级回退"，不会出现部分执行
// 后才失败的情形。运行期若仍遇到 ErrUnsupported，用 ShouldFallback 接住并整条回退。
func Supported(command string) bool {
	tokens, err := newLexer(command).lex()
	if err != nil {
		return false
	}
	f, err := parse(tokens)
	if err != nil {
		return false
	}
	return !containsUnsupported(f.Stmt)
}

func copyVars(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
