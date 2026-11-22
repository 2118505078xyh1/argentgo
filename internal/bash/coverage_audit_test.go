package bash

import (
	"fmt"
	"testing"
)

// commonCommands 是一份较广的常用 shell 命令清单，用于审计内建覆盖率。
// 它既包含 shell 内建，也包含高频外部工具；不要求 100% 覆盖（平台相关与
// 协议类命令有意为桩），但第一档高频命令应全部内置。
var commonCommands = []string{
	// 文件/目录/查找
	"pwd", "cd", "ls", "mkdir", "rmdir", "rm", "cp", "mv", "touch", "ln",
	"basename", "dirname", "find", "locate", "whereis", "du", "df", "stat",
	"readlink", "realpath", "file",
	// 文本处理
	"cat", "less", "more", "head", "tail", "wc", "nl", "sort", "uniq", "cut", "tr",
	"grep", "sed", "awk", "tee", "xargs", "tac", "diff", "comm", "paste",
	"rev", "fold", "expand",
	// shell 内建/控制
	"echo", "printf", "read", "test", "[", "true", "false", ":", "export", "unset",
	"set", "alias", "unalias", "history", "source", ".", "eval", "exec", "exit",
	"return", "break", "continue", "shift", "local", "declare", "readonly",
	"type", "command", "jobs", "fg", "bg", "wait", "kill", "getopts", "trap",
	// 进程/系统
	"ps", "top", "htop", "killall", "pkill", "nohup", "uname", "hostname", "date",
	"whoami", "id", "uptime", "free", "who", "w", "dmesg", "env",
	// 网络
	"curl", "wget", "ping", "ss", "netstat", "ip", "ifconfig", "dig", "nslookup",
	"ssh", "scp",
	// 压缩
	"tar", "zip", "unzip", "gzip", "gunzip",
	// 小工具
	"which", "man", "help", "expr", "bc", "sleep", "seq", "yes", "shuf", "mktemp",
	"bzip2", "xz", "unxz", "time",
}

// TestBuiltinCoverage 报告常用命令的内建覆盖率（只记录不失败）。
// 用它追踪逐批补内建的进度；真正的临界命令由各功能测试守护。
func TestBuiltinCoverage(t *testing.T) {
	seen := map[string]bool{}
	var have, missing []string
	for _, c := range commonCommands {
		if seen[c] {
			continue
		}
		seen[c] = true
		if isBuiltin(c) {
			have = append(have, c)
		} else {
			missing = append(missing, c)
		}
	}
	pct := float64(len(have)) / float64(len(seen)) * 100
	fmt.Printf("\n内建覆盖率: %d/%d = %.1f%%\n", len(have), len(seen), pct)
	if len(missing) > 0 {
		fmt.Printf("未内置: %v\n", missing)
	}
}
