package cli

import (
	"os"
	"path/filepath"
	"strings"
)

// workspaceRoot 把 -C/--cwd（或进程目录）解析为子命令使用的绝对路径。
func workspaceRoot(app App, cwd string) (string, error) {
	if strings.TrimSpace(cwd) == "" {
		cwd, _ = os.Getwd()
	}
	return filepath.Abs(cwd)
}

// homeFromEnv 找到用户主目录，用于 skill/command 的发现。
func homeFromEnv(env func(string) string) string {
	for _, key := range []string{"HOME", "USERPROFILE"} {
		if value := strings.TrimSpace(env(key)); value != "" {
			return value
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// splitRoot 从子命令参数中移除开头的 -C/--cwd flag。
func splitRoot(args []string) (cwd string, rest []string, err error) {
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "-C" || args[i] == "--cwd":
			if i+1 >= len(args) {
				return "", nil, &usageError{msg: args[i] + " needs a value"}
			}
			cwd = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--cwd="):
			cwd = strings.TrimPrefix(args[i], "--cwd=")
		case strings.HasPrefix(args[i], "-C="):
			cwd = strings.TrimPrefix(args[i], "-C=")
		default:
			rest = append(rest, args[i])
		}
	}
	return cwd, rest, nil
}
