package cli

import (
	"context"
	"fmt"
	"io"
)

// runContext 实现 `context [flags]`：打印一份"上下文中有什么"的只读报告。
//
// 它镜像 runUsage 的退出码约定：-h/--help 打印本命令 usage 并返回 0；出现位置
// 参数返回 2；未注入 ContextReport 时返回 1；报告生成本身失败同样返回 1。
//
// 依赖说明：本函数引用 App.ContextReport 字段，该字段由主接线
// （internal/cli/cli.go 的 App 结构与命令分发）注入。预期签名与 TokenReporter
// 一致：
//
//	ContextReport func(opts Options) (string, error)
//
// 主接线通常把它绑成闭包：func(opts Options) (string, error) {
// return app.ContextReport(stdio, opts) }，其中被调函数是 internal/app 的
// ContextReport(stdio, opts)。
//
// 在该字段加入 App 之前本文件无法编译；主会话需要在 App 上添加该字段，并把
// "context" 子命令分发到本函数。
func runContext(app App, _ context.Context, args []string) int {
	opts, positional, err := parseArgs(args, app.env, app.IO.Err)
	if err != nil {
		return 2
	}
	if opts.Help {
		contextUsage(app.IO.Out)
		return 0
	}
	if len(positional) > 0 {
		fmt.Fprintf(app.IO.Err, "argent: context: unexpected argument %q\n", positional[0])
		contextUsage(app.IO.Err)
		return 2
	}
	if app.ContextReport == nil {
		fmt.Fprintln(app.IO.Err, "error: context report not configured")
		return 1
	}
	report, err := app.ContextReport(*opts)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	fmt.Fprint(app.IO.Out, report)
	return 0
}

// contextUsage 打印 context 子命令的说明。
func contextUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argent context [flags]

Print a read-only report of what is currently in the model context: injected
memory documents, skills, commands, tools and the active sandbox/approval
posture. The report never modifies files.
`)
}
