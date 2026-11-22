package bash

import (
	"fmt"
	"sort"
	"strings"
)

// sysBuiltinNames 是本文件新增的系统服务/包管理/帮助类内建命令。
// Windows 没有这些命令的真实对应物，这里一律给出明确的中文响应，
// 绝不返回 "command not found"，也不静默失败。
var sysBuiltinNames = map[string]bool{
	"apt": true, "apt-get": true, "yum": true,
	"dnf": true, "pacman": true, "brew": true,
	"systemctl": true, "journalctl": true, "crontab": true,
	"man": true, "su": true, "sudo": true, "passwd": true, "help": true,
}

// builtinBrief 为 man / help 提供每条内建命令的简要中文说明。
// 键覆盖 shell 内建、coreutils 子集与本文件新增命令。
var builtinBrief = map[string]string{
	// shell 内建。
	"cd": "切换当前工作目录（限制在工作区内）", "pwd": "打印当前工作目录",
	"echo": "输出文本到标准输出", "export": "设置或导出环境变量",
	"unset": "删除环境变量", "true": "始终返回退出码 0",
	"false": "始终返回退出码 1", "local": "在函数作用域内定义局部变量",
	"return": "从函数中返回并设定退出码", "break": "跳出最近的循环",
	"continue": "进入下一次循环", "shift": "左移位置参数", ":": "空命令（退出码 0）",
	"read": "从标准输入读取一行", "source": "在当前 shell 中执行脚本文件",
	".": "source 的简写形式", "eval": "将参数作为命令重新解析执行",
	"alias": "定义或列出命令别名", "unalias": "删除命令别名",
	"history": "查看命令历史", "env": "显示或临时设置环境变量",
	"which": "定位命令来源", "command": "执行命令并绕过函数/别名", "exit": "退出 shell",
	// coreutils 子集。
	"ls": "列出目录内容", "cat": "连接并输出文件内容",
	"grep": "按模式过滤文本行", "wc": "统计行数、词数、字节数",
	"head": "输出文件开头部分", "tail": "输出文件末尾部分",
	"mkdir": "创建目录", "rm": "删除文件或目录",
	"printf": "格式化输出", "test": "求值条件表达式",
	"[": "test 的括号形式", "touch": "创建空文件或更新时间戳",
	"cp": "复制文件", "mv": "移动或重命名文件", "tee": "从标准输入读取并同时写出",
	// 本文件新增命令。
	"apt": "包管理桩（Windows 下不提供，仅提示）", "apt-get": "包管理桩（同 apt）",
	"yum": "包管理桩（同 apt）", "dnf": "包管理桩（同 apt）",
	"pacman": "包管理桩（同 apt）", "brew": "包管理桩（同 apt）",
	"systemctl":  "systemd 服务管理桩（当前平台无 systemd）",
	"journalctl": "systemd 日志查看桩（当前平台无 journal）",
	"crontab":    "cron 计划任务桩（当前平台无 cron）",
	"man":        "查看内建命令的简要手册", "su": "用户切换桩（内建 shell 不支持）",
	"sudo": "提权桩（内建 shell 不支持）", "passwd": "改密桩（内建 shell 不支持）",
	"help": "列出所有内建命令名",
}

// runSysBuiltin 是本文件新增命令的统一分派入口，便于 runBuiltin 一行接线。
// 第四个返回值表示 name 是否由本文件负责；调用方仅在为 true 时使用前三个返回值。
func (it *Interp) runSysBuiltin(name string, args []string, stdin string) (string, string, error, bool) {
	_ = stdin // 本批命令均不消费标准输入，保留参数以对齐内建签名。
	if !sysBuiltinNames[name] {
		return "", "", nil, false
	}
	switch name {
	case "apt", "apt-get", "yum", "dnf", "pacman", "brew":
		out, errOut, err := it.biPackageManager(name, args)
		return out, errOut, err, true
	case "systemctl":
		out, errOut, err := it.biSystemctl(args)
		return out, errOut, err, true
	case "journalctl":
		out, errOut, err := it.biJournalctl(args)
		return out, errOut, err, true
	case "crontab":
		out, errOut, err := it.biCrontab(args)
		return out, errOut, err, true
	case "man":
		out, errOut, err := it.biMan(args)
		return out, errOut, err, true
	case "su", "sudo":
		out, errOut, err := it.biSwitchUser(name, args)
		return out, errOut, err, true
	case "passwd":
		out, errOut, err := it.biPasswd(args)
		return out, errOut, err, true
	case "help":
		out, errOut, err := it.biHelp()
		return out, errOut, err, true
	}
	return "", "", nil, false
}

// biPackageManager 处理 apt/apt-get/yum/dnf/pacman/brew。
// Windows 没有这些包管理器：install/remove/update 等子命令给出明确中文提示并退出 1；
// --version/-v 返回内建桩版本串，让用户知道这是有意为之的 stub 而非命令缺失。
func (it *Interp) biPackageManager(name string, args []string) (string, string, error) {
	// 版本查询单独放行，退出码 0，证明命令确实已内置。
	if len(args) > 0 && (args[0] == "--version" || args[0] == "-v") {
		it.LastStatus = 0
		return "argentgo builtin package manager shim 1.0\n", "", nil
	}
	sub := "（未指定子命令）"
	if len(args) > 0 {
		sub = args[0]
	}
	it.LastStatus = 1
	msg := fmt.Sprintf("shell: %s: 当前为 Windows 环境，内建 shell 不提供包管理器；"+
		"请在 WSL 或原生 Linux/macOS 中使用。子命令 %q 不可用"+
		"（可识别：install/remove/update/upgrade/search/list/info/--version）。\n", name, sub)
	return "", msg, nil
}

// biSystemctl 处理 systemd 服务管理。当前平台无 systemd：
// status/start/stop 等子命令给出明确中文提示并退出 1，--version 返回内建桩版本串。
func (it *Interp) biSystemctl(args []string) (string, string, error) {
	if len(args) > 0 && (args[0] == "--version" || args[0] == "-v") {
		it.LastStatus = 0
		return "argentgo builtin systemctl shim 1.0\n", "", nil
	}
	sub := "（未指定子命令）"
	if len(args) > 0 {
		sub = args[0]
	}
	it.LastStatus = 1
	msg := fmt.Sprintf("shell: systemctl: 当前平台无 systemd，无法执行 %q；"+
		"请改用 Windows 服务管理工具（sc.exe / PowerShell），或在 WSL/Linux 中运行。"+
		"（可识别：status/start/stop/restart/enable/disable/--version）。\n", sub)
	return "", msg, nil
}

// biJournalctl 处理 systemd journal 查询。当前平台无 journal，
// 不论是否带 -u/--unit 都给出明确中文提示并退出 1。
func (it *Interp) biJournalctl(args []string) (string, string, error) {
	// 手动解析 -u svc / --unit=svc，避免英文 flag 错误破坏"全中文响应"的承诺。
	unit := ""
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "-u" && i+1 < len(args):
			i++
			unit = args[i]
		case strings.HasPrefix(args[i], "--unit="):
			unit = strings.TrimPrefix(args[i], "--unit=")
		}
	}
	it.LastStatus = 1
	if unit != "" {
		return "", fmt.Sprintf("shell: journalctl: 当前平台无 systemd journal，"+
			"无法查看单元 %q 的日志；请在 WSL/Linux 中使用。\n", unit), nil
	}
	return "", "shell: journalctl: 当前平台无 systemd journal，" +
		"无法读取系统日志；请在 WSL/Linux 中使用。\n", nil
}

// biCrontab 处理 cron 计划任务。当前平台无 cron：
// -l 显示空列表并说明（退出 0），-e/-r 等给出明确中文提示并退出 1。
func (it *Interp) biCrontab(args []string) (string, string, error) {
	if len(args) == 1 && args[0] == "-l" {
		it.LastStatus = 0
		return "# 当前平台无 cron，内建 shell 的计划任务列表为空（无 crontab 条目）\n", "", nil
	}
	op := "（未指定选项）"
	if len(args) > 0 {
		op = args[0]
	}
	it.LastStatus = 1
	return "", fmt.Sprintf("shell: crontab: 当前平台无 cron，内建 shell 不支持 %q 操作；"+
		"请在 WSL/Linux 中使用（可用选项：-l 查看空列表、-e 编辑）。\n", op), nil
}

// biMan 提供内建命令的简要手册：无参数时概览兼容性矩阵，
// 给定已知命令时输出"命令名 - 简要说明"，未知命令时明确提示无手册并退出 1。
func (it *Interp) biMan(args []string) (string, string, error) {
	if len(args) == 0 {
		// 概览：列出兼容性矩阵，说明哪些语法原生支持、哪些须回退系统 shell。
		var b strings.Builder
		b.WriteString("内建 shell 帮助：使用 man <命令> 查看具体命令的简要说明；help 列出全部命令名。\n")
		b.WriteString("兼容性矩阵：\n")
		for _, e := range CompatibilityMatrix() {
			mode := "需回退系统 shell"
			if e.Native {
				mode = "原生支持"
			}
			fmt.Fprintf(&b, "  %s [%s]：%s\n", e.Syntax, mode, e.Note)
		}
		it.LastStatus = 0
		return b.String(), "", nil
	}
	name := args[0]
	brief, ok := builtinBrief[name]
	if !ok && (shellBuiltinNames[name] || coreutilNames[name] || sysBuiltinNames[name]) {
		brief = "内建 shell 命令"
		ok = true
	}
	if !ok {
		it.LastStatus = 1
		return "", fmt.Sprintf("shell: man: 内建 shell 无该命令手册：%s\n", name), nil
	}
	it.LastStatus = 0
	return fmt.Sprintf("NAME\n    %s - %s\nSTATUS\n    内建命令（由 argentgo 内建 shell 提供，不依赖外部进程）。\n", name, brief), "", nil
}

// biSwitchUser 处理 su / sudo。两者在本环境中都没有真实语义：
// 给出明确中文错误并退出 1，绝不伪装成功或报命令缺失。
func (it *Interp) biSwitchUser(name string, args []string) (string, string, error) {
	_ = args // 参数不同不影响结论：一律不支持。
	it.LastStatus = 1
	return "", fmt.Sprintf("shell: %s: 内建 shell 不支持用户切换/提权；"+
		"当前环境无对应机制，请在真实系统中使用。\n", name), nil
}

// biPasswd 处理 passwd。内建 shell 不支持修改系统密码，给出明确中文错误并退出 1。
func (it *Interp) biPasswd(args []string) (string, string, error) {
	_ = args
	it.LastStatus = 1
	return "", "shell: passwd: 内建 shell 不支持修改密码；" +
		"请在系统层面使用账户管理工具。\n", nil
}

// biHelp 列出所有内建命令名（shell 内建、coreutils 子集与本文件新增命令），
// 去重后按字母序输出，退出码 0。
func (it *Interp) biHelp() (string, string, error) {
	seen := make(map[string]bool, len(shellBuiltinNames)+len(coreutilNames)+len(sysBuiltinNames))
	for n := range shellBuiltinNames {
		seen[n] = true
	}
	for n := range coreutilNames {
		seen[n] = true
	}
	for n := range sysBuiltinNames {
		seen[n] = true
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("内建 shell 命令列表（按字母序）：\n")
	for _, n := range names {
		fmt.Fprintf(&b, "  %s\n", n)
	}
	b.WriteString("使用 man <命令> 查看简要说明。\n")
	it.LastStatus = 0
	return b.String(), "", nil
}
