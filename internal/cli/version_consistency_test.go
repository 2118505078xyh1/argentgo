package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// 本文件守护发布版本字面量在 cli.go / mcp.go / lsp/client.go / tui/welcome.go
// 四处的一致性，防止只更新权威的 cli.Version 却漏改 MCP / LSP 客户端和欢迎卡报告的版本。
// 四个文件的版本字面量必须完全一致；任一处改错、删掉或格式走样都会让测试失败。

// versionLiteralSpec 描述一个文件中版本字面量的提取方式。
type versionLiteralSpec struct {
	relPath string
	pattern *regexp.Regexp
}

// versionLiteralSources 是需要保持一致的四个版本字面量来源。
// cli.go 的 const Version 是权威值，其余三处必须与之相等。
var versionLiteralSources = []versionLiteralSpec{
	{"internal/cli/cli.go", regexp.MustCompile(`const\s+Version\s*=\s*"([^"]+)"`)},
	{"internal/mcp/mcp.go", regexp.MustCompile(`clientVersion\s*=\s*"([^"]+)"`)},
	{"internal/lsp/client.go", regexp.MustCompile(`"version"\s*:\s*"([^"]+)"`)},
	{"internal/tui/welcome.go", regexp.MustCompile(`welcomeVersion\s*=\s*"([^"]+)"`)},
}

// versionGuardRepoRoot 从当前包目录向上找到 go.mod，作为三个文件相对路径的根。
func versionGuardRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("取工作目录失败: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("向上遍历未找到 go.mod")
		}
		dir = parent
	}
}

// extractVersionLiteral 读取相对路径文件并按正则提取第一个捕获组的版本字面量。
func extractVersionLiteral(t *testing.T, root string, spec versionLiteralSpec) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(spec.relPath))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", spec.relPath, err)
	}
	m := spec.pattern.FindStringSubmatch(string(data))
	if len(m) < 2 {
		t.Fatalf("在 %s 中未找到版本字面量（pattern=%s）", spec.relPath, spec.pattern.String())
	}
	return m[1]
}

// TestReportedClientVersionsConsistent 保证 cli.Version、MCP 的 clientVersion、
// LSP clientInfo 的 version 与 TUI 欢迎卡的版本字面量完全一致。
func TestReportedClientVersionsConsistent(t *testing.T) {
	root := versionGuardRepoRoot(t)
	versions := make(map[string]string, len(versionLiteralSources))
	for _, spec := range versionLiteralSources {
		versions[spec.relPath] = extractVersionLiteral(t, root, spec)
	}

	want := versions["internal/cli/cli.go"]
	for _, spec := range versionLiteralSources[1:] {
		if got := versions[spec.relPath]; got != want {
			t.Errorf("%s 的版本字面量 %q 与权威 cli.Version %q 不一致", spec.relPath, got, want)
		}
	}
}
