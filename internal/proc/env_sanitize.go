package proc

import (
	"os"
	"runtime"
	"strings"
)

// hostPrivateEnvNames 是把宿主会话管道带入子进程的环境变量。
// 继承它们的嵌套 argent agent 可能误把它们当作自己的会话状态，
// 因此在生成子进程之前，会把它们从
// 继承的环境中移除。
var hostPrivateEnvNames = []string{
	"ARGENT_SESSION_ID",
	"ARGENT_RUN_ID",
}

// hostPrivateEnvPrefixes 是出于同样原因而属于宿主私有的名称前缀：
// ARGENT_PARENT_* 和 SUBAGENT_PARENT_* 由宿主在
// 启动子 agent 时写入。
var hostPrivateEnvPrefixes = []string{
	"ARGENT_PARENT_",
	"SUBAGENT_PARENT_",
}

// SanitizeEnv 返回子进程应接收的环境：父进程环境中
// 移除宿主私有变量，后跟 extra。
// extra 中的条目原样追加且从不被过滤，因此显式的
// 每次调用值总是胜过任何继承值。顺序保持不变。
//
// 本包中的两条生成路径（proc.Run 以及
// RunWithTimeout/StartDetached 使用的分离路径）都应用它。代码树中
// 其他生成子进程的位置（jobs、MCP、LSP、pty、hooks）以后可以采用它。
func SanitizeEnv(extra []string) []string {
	parent := os.Environ()
	out := make([]string, 0, len(parent)+len(extra))
	for _, entry := range parent {
		if isHostPrivateEnvEntry(entry) {
			continue
		}
		out = append(out, entry)
	}
	return append(out, extra...)
}

// isHostPrivateEnvEntry 报告继承条目的键是否在宿主私有
// 黑名单中。键是第一个 '=' 之前的全部内容（对于裸 "KEY" 则是
// 整条），因此值中含 '=' 不会混淆
// 切分。
func isHostPrivateEnvEntry(entry string) bool {
	key := entry
	if i := strings.IndexByte(entry, '='); i >= 0 {
		key = entry[:i]
	}
	for _, name := range hostPrivateEnvNames {
		if envNamesEqual(key, name) {
			return true
		}
	}
	for _, prefix := range hostPrivateEnvPrefixes {
		if envNameHasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// envNamesEqual 比较两个环境变量名。Windows 不区分大小写，
// 其他平台区分大小写。
func envNamesEqual(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// envNameHasPrefix 报告在该平台的名称比较规则下，key 是否以
// prefix 开头。
func envNameHasPrefix(key, prefix string) bool {
	if runtime.GOOS == "windows" {
		return strings.HasPrefix(strings.ToLower(key), strings.ToLower(prefix))
	}
	return strings.HasPrefix(key, prefix)
}
