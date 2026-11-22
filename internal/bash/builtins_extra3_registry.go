package bash

// 本文件注册第二档补充的常用内建命令（第一档 27 个），由并行写手实现，
// 统一用三参数签名，这里只做接线。

// extraBuiltinNames2 是第三批补充的命令名。
var extraBuiltinNames2 = map[string]bool{
	// shell 内建控制类（builtins_control.go）
	"set": true, "exec": true, "declare": true, "typeset": true,
	"readonly": true, "type": true, "wait": true, "getopts": true, "trap": true,
	// 文件信息/小工具（coreutils_misc2.go）
	"stat": true, "readlink": true, "realpath": true, "file": true,
	"seq": true, "yes": true, "shuf": true, "mktemp": true,
	// 计算器/压缩桩（coreutils_misc3.go）
	"expr": true, "bc": true, "sleep": true,
	"bzip2": true, "xz": true, "unxz": true,
	// 文本小工具（coreutils_text3.go）
	"tac": true, "diff": true, "comm": true, "paste": true, "rev": true,
	"fold": true, "expand": true,
}

func (it *Interp) runExtraBuiltin2(name string, args []string, stdin string) (string, string, error, bool) {
	if !extraBuiltinNames2[name] {
		return "", "", nil, false
	}
	switch name {
	case "set":
		return handled3(it.biSet(args, stdin))
	case "exec":
		return handled3(it.biExec(args, stdin))
	case "declare":
		return handled3(it.biDeclare(args, stdin))
	case "typeset":
		return handled3(it.biTypeset(args, stdin))
	case "readonly":
		return handled3(it.biReadonly(args, stdin))
	case "type":
		return handled3(it.biType(args, stdin))
	case "wait":
		return handled3(it.biWait(args, stdin))
	case "getopts":
		return handled3(it.biGetopts(args, stdin))
	case "trap":
		return handled3(it.biTrap(args, stdin))
	// —— coreutils_misc2.go ——
	case "stat":
		return handled3(it.biStat(args, stdin))
	case "readlink":
		return handled3(it.biReadlink(args, stdin))
	case "realpath":
		return handled3(it.biRealpath(args, stdin))
	case "file":
		return handled3(it.biFile(args, stdin))
	case "seq":
		return handled3(it.biSeq(args, stdin))
	case "yes":
		return handled3(it.biYes(args, stdin))
	case "shuf":
		return handled3(it.biShuf(args, stdin))
	case "mktemp":
		return handled3(it.biMktemp(args, stdin))
	// —— coreutils_misc3.go ——
	case "expr":
		return handled3(it.biExpr(args, stdin))
	case "bc":
		return handled3(it.biBc(args, stdin))
	case "sleep":
		return handled3(it.biSleep(args, stdin))
	case "bzip2":
		return handled3(it.biBzip2(args, stdin))
	case "xz":
		return handled3(it.biXz(args, stdin))
	case "unxz":
		return handled3(it.biUnxz(args, stdin))
	// —— coreutils_text3.go ——
	case "tac":
		return handled3(it.biTac(args, stdin))
	case "diff":
		return handled3(it.biDiff(args, stdin))
	case "comm":
		return handled3(it.biComm(args, stdin))
	case "paste":
		return handled3(it.biPaste(args, stdin))
	case "rev":
		return handled3(it.biRev(args, stdin))
	case "fold":
		return handled3(it.biFold(args, stdin))
	case "expand":
		return handled3(it.biExpand(args, stdin))
	}
	return "", "", nil, false
}
