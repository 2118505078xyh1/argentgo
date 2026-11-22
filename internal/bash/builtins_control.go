package bash

import (
	"fmt"
	"sort"
	"strings"
)

// 本文件实现一组 shell 内建控制类命令（set/type/declare/typeset/readonly/
// exec/trap/wait/getopts）。它们完全由解释器自身完成，不依赖外部程序或 PATH。
//
// 状态记录约定：由于不能在已有文件里给 Interp 增加字段，set 的 -e/-u/-x
// 开关以及 declare/readonly 的只读标记都写入 it.Vars 的特殊键中。这些开关
// 仅作记录并在 `set` 的输出中体现，不触发真正的执行中断或只读拦截——
// 真正的语义需要改动解释器的执行路径，由其他文件负责。

// controlSignalNames 为 trap -l 提供的常见 POSIX 信号名列表（纯展示用）。
var controlSignalNames = []string{
	"SIGHUP", "SIGINT", "SIGQUIT", "SIGILL", "SIGTRAP", "SIGABRT",
	"SIGBUS", "SIGFPE", "SIGKILL", "SIGUSR1", "SIGSEGV", "SIGUSR2",
	"SIGPIPE", "SIGALRM", "SIGTERM", "SIGCHLD", "SIGCONT", "SIGSTOP",
	"SIGTSTP", "SIGTTIN", "SIGTTOU", "SIGURG", "SIGXCPU",
	"SIGXFSZ", "SIGVTALRM", "SIGPROF", "SIGWINCH", "SIGIO", "SIGPWR", "SIGSYS",
}

// biSet 实现 set：无参数时按字母序打印全部变量（含记录开关的特殊键）；
// `set -- args` 重设位置参数；-e/-u/-x（以及 +e/+u/+x）只把开关状态写入
// it.Vars 的特殊键，不做真正的 errexit/nounset/xtrace 执行中断。
func (it *Interp) biSet(args []string, stdin string) (string, string, error) {
	_ = stdin // set 不消费标准输入，保留参数以对齐内建签名。
	if it.Vars == nil {
		it.Vars = make(map[string]string)
	}
	// 无参数：按 NAME=value 输出所有变量，set 选项状态随之可见。
	if len(args) == 0 {
		keys := make([]string, 0, len(it.Vars))
		for k := range it.Vars {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		for _, k := range keys {
			fmt.Fprintf(&b, "%s=%s\n", k, it.Vars[k])
		}
		it.LastStatus = 0
		return b.String(), "", nil
	}
	// set -- arg1 arg2 ...：重设位置参数。
	if args[0] == "--" {
		it.Args = append([]string(nil), args[1:]...)
		it.LastStatus = 0
		return "", "", nil
	}
	// 逐个解析 -e/+e、-u/+u、-x/+x（也接受 -ex 这类组合短选项）。
	for _, a := range args {
		if len(a) < 2 || (a[0] != '-' && a[0] != '+') {
			continue
		}
		on := a[0] == '-'
		for _, c := range a[1:] {
			var key string
			switch c {
			case 'e':
				key = "__ARGENT_SET_E" // errexit 记录键。
			case 'u':
				key = "__ARGENT_SET_U" // nounset 记录键。
			case 'x':
				key = "__ARGENT_SET_X" // xtrace 记录键。
			default:
				continue
			}
			if on {
				it.Vars[key] = "1"
			} else {
				it.Vars[key] = "0"
			}
		}
	}
	it.LastStatus = 0
	return "", "", nil
}

// biType 显示每个名字的类型：alias/function/builtin/keyword。
// 本 shell 无 PATH 外部查找能力，无法归类的名字一律报告“未找到”并返回 1。
func (it *Interp) biType(args []string, stdin string) (string, string, error) {
	_ = stdin // type 不消费标准输入，保留参数以对齐内建签名。
	if len(args) == 0 {
		return it.failCmd("type", 2, fmt.Errorf("缺少命令名"))
	}
	var b strings.Builder
	status := 0
	for _, name := range args {
		kind := ""
		switch {
		case it.Aliases != nil && it.Aliases[name] != "":
			kind = "alias"
		case it.Funcs[name] != nil:
			kind = "function"
		case isBuiltin(name):
			kind = "builtin"
		case isReservedWord(name):
			kind = "keyword"
		default:
			kind = "未找到" // 无 PATH 查找：一律视为找不到。
			status = 1
		}
		fmt.Fprintf(&b, "%s: %s\n", name, kind)
	}
	it.LastStatus = status
	return b.String(), "", nil
}

// biDeclare 实现 declare/typeset：支持 -r（只读）与 -x（导出）标记。
// -x 等价于直接写入变量表；-r 仅以 __ARGENT_RO_ 前缀的特殊键记录声明并
// 给出中文提示，真正的只读拦截在其他文件的赋值路径完成。
func (it *Interp) biDeclare(args []string, stdin string) (string, string, error) {
	_ = stdin // declare 不消费标准输入，保留参数以对齐内建签名。
	if it.Vars == nil {
		it.Vars = make(map[string]string)
	}
	readonly, export := false, false
	i := 0
	// 解析开头的短选项（-r/-x，可组合如 -rx）。
	for ; i < len(args) && strings.HasPrefix(args[i], "-") && len(args[i]) > 1; i++ {
		for _, c := range args[i][1:] {
			switch c {
			case 'r':
				readonly = true
			case 'x':
				export = true
			}
		}
	}
	_ = export // 本 shell 中导出与写入变量表等价，无需额外处理。
	if i == len(args) {
		it.LastStatus = 0
		return "", "", nil
	}
	var hints strings.Builder
	for _, a := range args[i:] {
		name, val, hasVal := strings.Cut(a, "=")
		if name == "" {
			continue
		}
		if hasVal {
			it.Vars[name] = val
		} else if _, ok := it.Vars[name]; !ok {
			it.Vars[name] = "" // 无赋值的新变量初始化为空。
		}
		if readonly {
			it.Vars["__ARGENT_RO_"+name] = "1"
			fmt.Fprintf(&hints, "shell: declare: %s 已标记为只读\n", name)
		}
	}
	it.LastStatus = 0
	return hints.String(), "", nil
}

// biTypeset 与 declare 同义：完整复用 biDeclare 的选项解析与赋值逻辑，
// 仅保留独立入口以便注册表按命令名分派。
func (it *Interp) biTypeset(args []string, stdin string) (string, string, error) {
	return it.biDeclare(args, stdin)
}

// biReadonly 把 NAME[=val] 标记为只读：写入变量并用 __ARGENT_RO_ 前缀键
// 记录标记，同时给出中文提示。真正的只读检查在其他文件完成，这里只做声明。
func (it *Interp) biReadonly(args []string, stdin string) (string, string, error) {
	_ = stdin // readonly 不消费标准输入，保留参数以对齐内建签名。
	if len(args) == 0 {
		return it.failCmd("readonly", 2, fmt.Errorf("缺少变量名"))
	}
	if it.Vars == nil {
		it.Vars = make(map[string]string)
	}
	var b strings.Builder
	for _, a := range args {
		name, val, hasVal := strings.Cut(a, "=")
		if name == "" {
			continue
		}
		if hasVal {
			it.Vars[name] = val
		} else if _, ok := it.Vars[name]; !ok {
			it.Vars[name] = ""
		}
		// 特殊前缀键记录只读标记；实际赋值拦截由其他文件负责。
		it.Vars["__ARGENT_RO_"+name] = "1"
		fmt.Fprintf(&b, "shell: readonly: %s 已标记为只读\n", name)
	}
	it.LastStatus = 0
	return b.String(), "", nil
}

// biExec 用 parseCommandString + it.Run 在当前解释器作用域执行给定命令，
// 然后给出提示。工具场景下不会真的替换当前进程。
func (it *Interp) biExec(args []string, stdin string) (string, string, error) {
	_ = stdin // 重解析后的命令自行处理输入，这里不直接消费 stdin。
	if len(args) == 0 {
		it.LastStatus = 0
		return "", "", nil
	}
	f, err := parseCommandString(strings.Join(args, " "))
	if err != nil {
		return it.failCmd("exec", 2, err)
	}
	out, err := it.Run(f)
	if err != nil {
		return "", out, err
	}
	// 命令已执行；exec 的工具语义不替换进程，只补充提示。
	return out + "shell: exec 已执行（工具场景不替换进程）\n", "", nil
}

// biTrap 是信号陷阱的桩命令：trap -l 列出信号名；其余调用给出明确中文
// 提示，说明内建 shell 无 trap 机制，绝不返回 command-not-found。
func (it *Interp) biTrap(args []string, stdin string) (string, string, error) {
	_ = stdin // trap 不消费标准输入，保留参数以对齐内建签名。
	if len(args) > 0 && args[0] == "-l" {
		it.LastStatus = 0
		return strings.Join(controlSignalNames, " ") + "\n", "", nil
	}
	it.LastStatus = 0
	return "内建 shell 无信号 trap 机制\n", "", nil
}

// biWait 是后台作业等待的桩命令。无论是否给出 PID，都给出明确中文提示；
// 内建 shell 没有作业表，也没有可等待的后台进程。
func (it *Interp) biWait(args []string, stdin string) (string, string, error) {
	_ = stdin // wait 不消费标准输入，保留参数以对齐内建签名。
	if len(args) > 0 {
		// 即便给出 PID 也无作业表可查，提示保持一致并回显 PID。
		it.LastStatus = 0
		return fmt.Sprintf("内建 shell 无后台作业等待（PID: %s）\n", args[0]), "", nil
	}
	it.LastStatus = 0
	return "内建 shell 无后台作业等待\n", "", nil
}

// biGetopts 解析位置参数中的短选项：从 it.Args[0] 的 -x 形式里取出首个
// 合法选项字符，写入 VAR（并在需要时设置 OPTARG），随后左移位置参数。
// 解析到选项返回 0；位置参数耗尽或遇到非选项词时返回 1。
func (it *Interp) biGetopts(args []string, stdin string) (string, string, error) {
	_ = stdin // getopts 不消费标准输入，保留参数以对齐内建签名。
	if len(args) < 2 {
		return it.failCmd("getopts", 2, fmt.Errorf("用法: getopts OPTSTRING VAR"))
	}
	optstring, varName := args[0], args[1]
	if it.Vars == nil {
		it.Vars = make(map[string]string)
	}
	// 无参数或遇到非选项词：解析结束。
	if len(it.Args) == 0 || !strings.HasPrefix(it.Args[0], "-") || it.Args[0] == "-" || it.Args[0] == "--" {
		it.LastStatus = 1
		return "", "", nil
	}
	arg := it.Args[0]
	// 每次只处理一个选项字符（多字符组合如 -ab 取第一个合法字符）。
	var opt byte
	for i := 1; i < len(arg); i++ {
		if strings.IndexByte(optstring, arg[i]) >= 0 {
			opt = arg[i]
			break
		}
	}
	if opt == 0 {
		it.LastStatus = 1
		return "", "", nil
	}
	idx := strings.IndexByte(optstring, opt)
	takesArg := idx >= 0 && idx+1 < len(optstring) && optstring[idx+1] == ':'
	it.Vars[varName] = string(opt)
	if takesArg {
		// 带参数的选项：简化实现只取下一个位置参数作为 OPTARG。
		if len(it.Args) > 1 {
			it.Vars["OPTARG"] = it.Args[1]
			it.Args = it.Args[2:]
		} else {
			it.Vars["OPTARG"] = ""
			it.Args = it.Args[1:]
		}
	} else {
		it.Vars["OPTARG"] = ""
		it.Args = it.Args[1:]
	}
	it.LastStatus = 0
	return "", "", nil
}
