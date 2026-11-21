package cli

import "os"

// hasTerminal 报告 TUI 是否有可连接的终端。stdin 或 stdout 任一为交互式
// 即可：当只有 stdout 是终端时（例如 `echo foo | argentgo tui`），前端可以
// 重新打开控制 TTY 用于输入。注入的 isTerminal 使测试无需真实控制台
// 就能检验这道门控。
func hasTerminal(isTerminal func(uintptr) bool, stdin, stdout uintptr) bool {
	return isTerminal(stdin) || isTerminal(stdout)
}

// defaultHasTerminal 是 OSIO 接入的真实平台检查。它对应 Crush 根命令顶部的
// 守卫：没有它，流被重定向且无控制台的调用仍会加载配置并启动全屏 UI。
func defaultHasTerminal() bool {
	return hasTerminal(isTerminal, os.Stdin.Fd(), os.Stdout.Fd())
}

// hasTerminal 报告该 App 是否有交互式终端。测试构建的 IO 可以桩掉这个检查；
// nil 接缝表示"无可用终端"，从而阻止仅限终端的命令触碰配置或真实控制台。
func (a App) hasTerminal() bool {
	if a.IO.HasTerminal != nil {
		return a.IO.HasTerminal()
	}
	return false
}
