package cli

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/uri"
)

// runDoctorLinks 实现 `argentgo doctor --links`：扫描工作区内的 Markdown 文件，
// 提取 argent:// 引用并校验格式（uri.Parse/IsURI）与 scheme 注册情况
// （uri.Schemes）。它只读、不联网、不改变其它 doctor 检查与退出码语义。
func runDoctorLinks(app App) int {
	root, err := os.Getwd()
	if err != nil {
		root = "."
	}
	var files []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // 忽略不可读目录
		}
		if d.IsDir() {
			base := d.Name()
			if base == ".git" || base == "node_modules" || base == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.EqualFold(filepath.Ext(p), ".md") {
			files = append(files, p)
		}
		return nil
	})
	sort.Strings(files)

	// 注册 docs scheme：CLI 侧用仓库 docs/ 目录（native 二进制可用 go:embed
	// 注入自己的副本）。注册失败不影响格式校验，仅给出提示。
	if h, err := uri.NewDocsHandler(os.DirFS(filepath.Join(root, "docs"))); err == nil {
		_ = uri.RegisterDefault("docs", h)
	}

	registered := make(map[string]bool)
	for _, s := range uri.Schemes() {
		registered[s] = true
	}

	total, invalid, unregistered := 0, 0, 0
	var problems []string
	for _, file := range files {
		f, err := os.Open(file)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		line := 0
		for scanner.Scan() {
			line++
			for _, ref := range extractURIRefs(scanner.Text()) {
				total++
				scheme, _, err := uri.Parse(ref)
				if err != nil || !uri.IsURI(ref) {
					invalid++
					problems = append(problems, fmt.Sprintf("%s:%d: invalid %q", file, line, ref))
					continue
				}
				if !registered[scheme] {
					unregistered++
					problems = append(problems, fmt.Sprintf("%s:%d: unregistered scheme %q", file, line, scheme))
				} else if _, err := uri.ResolveDefault(ref); err != nil && scheme == "docs" {
					// docs 引用应能由当前工作区的 docs/ 解析；解析不了给出提示。
					problems = append(problems, fmt.Sprintf("%s:%d: unresolved %q: %v", file, line, ref, err))
				}
			}
		}
		f.Close()
	}

	fmt.Fprintf(app.IO.Out, "argent:// links: %d refs in %d markdown files\n", total, len(files))
	fmt.Fprintf(app.IO.Out, "registered schemes: %s\n", strings.Join(uri.Schemes(), ", "))
	// 展示内嵌文档索引的大小（从磁盘 docs 构建，验证 EncodeIndex 往返可用）。
	if h, err := uri.NewDocsHandler(os.DirFS(filepath.Join(root, "docs"))); err == nil {
		if index, err := h.EncodeIndex(); err == nil {
			fmt.Fprintf(app.IO.Out, "docs index: %d files, %d bytes\n", len(h.List()), len(index))
			restored, err := uri.NewDocsHandlerFromIndex(index)
			if err != nil || len(restored.List()) != len(h.List()) {
				fmt.Fprintln(app.IO.Err, "argentgo: docs index round-trip failed")
			}
		}
	}
	if len(problems) > 0 {
		fmt.Fprintf(app.IO.Err, "found %d invalid and %d unregistered references:\n", invalid, unregistered)
		for _, p := range problems {
			fmt.Fprintln(app.IO.Err, "  "+p)
		}
		return 1
	}
	fmt.Fprintln(app.IO.Out, "all argent:// references valid")
	return 0
}

// extractURIRefs 从一行文本里提取所有 argent://... 引用。URI 以空白、反引号、
// 尖括号、圆括号或引号结束。
func extractURIRefs(line string) []string {
	const prefix = "argent://"
	var out []string
	for {
		i := strings.Index(line, prefix)
		if i < 0 {
			return out
		}
		rest := line[i:]
		end := strings.IndexAny(rest, " \t`<>\"'()],;")
		if end < 0 {
			end = len(rest)
		}
		out = append(out, rest[:end])
		line = rest[end:]
	}
}
