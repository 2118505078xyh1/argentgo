package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"example.com/argentgo/internal/config"
)

// configPathsReport 对比 CLI 的写入路径与引擎实际会读取的候选路径。
// 两者本应重合；不重合时用户 `config set` 的配置会静默失效。
type configPathsReport struct {
	writePath   string
	enginePaths []string
}

// newConfigPathsReport 计算给定工作区下的两条路径视图。cwd 为空时使用进程目录。
func newConfigPathsReport(app App, cwd string) configPathsReport {
	root, err := workspaceRoot(app, cwd)
	if err != nil {
		root = "."
	}
	return configPathsReport{
		writePath:   app.configPath(),
		enginePaths: config.CandidatePaths(root, app.env),
	}
}

// diverged 报告 CLI 写入路径是否不在引擎候选层里。
func (r configPathsReport) diverged() bool {
	for _, path := range r.enginePaths {
		if sameConfigFilePath(path, r.writePath) {
			return false
		}
	}
	return true
}

// warning 返回分歧警告文本；路径一致时为空。
func (r configPathsReport) warning() string {
	if !r.diverged() {
		return ""
	}
	return fmt.Sprintf("警告: 命令行写入的配置路径 %s 不在引擎读取路径中，在此配置不会生效", r.writePath)
}

// sameConfigFilePath 比较两条配置路径是否指向同一文件；Windows 大小写不敏感。
func sameConfigFilePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if a == b {
		return true
	}
	return filepath.Separator == '\\' && strings.EqualFold(a, b)
}

// writeConfigPathsText 供 `config path` 使用：第一行保留裸写入路径以兼容旧脚本，
// 随后附上 CLI 写入路径与引擎读取路径的显式标签。
func writeConfigPathsText(out, errOut io.Writer, r configPathsReport) {
	fmt.Fprintln(out, r.writePath)
	fmt.Fprintf(out, "命令行写入路径:  %s\n", r.writePath)
	fmt.Fprintln(out, "引擎读取路径（优先级从低到高）:")
	for i, path := range r.enginePaths {
		fmt.Fprintf(out, "  %d. %s\n", i+1, path)
	}
	if warning := r.warning(); warning != "" {
		fmt.Fprintln(errOut, warning)
	}
}
