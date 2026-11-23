package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/uri"
)

// runDocs 实现 `argentgo docs [list | show <name>]`：从 docs/ 目录（或二进制
// 内嵌的 docs scheme）列出或查看文档，无需记忆 Markdown 文件的完整路径。
func runDocs(app App, _ context.Context, args []string) int {
	// 注册 docs scheme（CLI 侧用仓库 docs/；native 二进制可用 go:embed 注入）。
	if h, err := uri.NewDocsHandler(os.DirFS(docRoot())); err == nil {
		_ = uri.RegisterDefault("docs", h)
	}
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		docsUsage(app.IO.Out)
		return 0
	}
	if len(args) == 0 || args[0] == "list" {
		list, err := listDocs()
		if err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		for _, n := range list {
			fmt.Fprintln(app.IO.Out, n)
		}
		return 0
	}
	if args[0] == "show" {
		if len(args) != 2 {
			fmt.Fprintln(app.IO.Err, "argentgo: docs show needs a name")
			docsUsage(app.IO.Err)
			return 2
		}
		res, err := uri.ResolveDefault("argent://docs/" + args[1])
		if err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		fmt.Fprint(app.IO.Out, res.Content)
		return 0
	}
	fmt.Fprintf(app.IO.Err, "argentgo: unknown docs subcommand %q\n", args[0])
	docsUsage(app.IO.Err)
	return 2
}

// docRoot 返回 docs 目录（当前工作目录下的 docs/；找不到时回退到可执行文件旁）。
func docRoot() string {
	if _, err := os.Stat("docs"); err == nil {
		return "docs"
	}
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "docs")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "."
}

// listDocs 列出 docs 目录下的 Markdown 文件（按名称排序）。
func listDocs() ([]string, error) {
	entries, err := os.ReadDir(docRoot())
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".md") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

func docsUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argentgo docs [list | show <name>]

Lists or prints bundled documentation by name (e.g. "show USER_GUIDE.md").
`)
}
