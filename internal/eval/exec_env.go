package eval

import "strings"

// 本文件提供构造「空环境 + 白名单」隔离环境的纯函数，用于把凭据从子进程环境中剔除。
// 它不读取当前进程环境、不启动进程，也不修改任何入参；与 execEnvironment / RunExecCheck
// 的现有行为完全解耦，目前没有调用方，仅供后续隔离测试蓝图使用。

// IsolatedEnv 从 base 中只保留 allow 列出的环境变量，按 base 中的原有顺序输出。
// 典型用法：IsolatedEnv(os.Environ(), DefaultIsolatedAllowlist(runtime.GOOS))。
//
// 匹配规则：
//   - allow 的每一项必须是变量名（不含 '='）；只有当条目以 "KEY=" 为前缀时才算命中，
//     因此 allow 中的 "PATH" 不会匹配 "MYPATH=x" 或 "PATH_EXTRA=x"。
//   - 匹配按大小写精确进行，不折叠大小写；这既让行为在 Windows/Unix 上一致，
//     也避免宽泛匹配意外放行名称相近的敏感变量。调用方应使用与 base 一致的大小写。
//   - base 中重复出现的同名 key 只保留第一次出现的条目。
//   - 没有 '=' 的畸形条目直接忽略。
//
// 返回顺序稳定（与 base 一致）；当 base 或 allow 为空时返回非 nil 的空切片。
// 该函数是纯函数：不修改 base、allow，也不读取全局环境。
func IsolatedEnv(base []string, allow []string) []string {
	if len(base) == 0 || len(allow) == 0 {
		return []string{}
	}

	// 把白名单变成集合；allow 自身含重复项也不影响结果。
	allowed := make(map[string]struct{}, len(allow))
	for _, key := range allow {
		allowed[key] = struct{}{}
	}

	seen := make(map[string]struct{}, len(base))
	out := make([]string, 0, len(base))
	for _, entry := range base {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, ok := allowed[key]; !ok {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, entry)
	}
	return out
}

// DefaultIsolatedAllowlist 返回跨平台「最小可运行」环境变量白名单：只放进程启动与
// 临时文件所必需的项，刻意不含任何凭据相关变量。
//
// Windows 必须包含 SystemRoot：缺失它会导致 CreateProcess 失败，子进程根本无法启动；
// TEMP/TMP 决定临时目录，PATH 用于定位 go 等可执行文件，USERPROFILE 是用户目录依据。
// Unix 侧 PATH 用于定位可执行文件，HOME 是用户目录，TMPDIR 决定临时目录。
//
// goos 取 runtime.GOOS 的取值；"windows"（不区分大小写）走 Windows 基线，
// 其余（包括未知值）按 Unix 基线处理。返回的是新切片，调用方可自由修改。
func DefaultIsolatedAllowlist(goos string) []string {
	if strings.EqualFold(goos, "windows") {
		return []string{"SystemRoot", "TEMP", "TMP", "PATH", "USERPROFILE"}
	}
	return []string{"PATH", "HOME", "TMPDIR"}
}
