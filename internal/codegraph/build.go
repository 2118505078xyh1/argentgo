package codegraph

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ignoredDirs 是遍历时的拒绝列表，与 repo_map 的忽略集合同源；
// 这些目录不参与建图，避免 vendor/dist 把图谱淹没在第三方符号里。
var ignoredDirs = map[string]bool{
	".git": true, "node_modules": true, "target": true, "dist": true,
	".argent": true, "vendor": true, ".idea": true, ".vscode": true,
	"build": true, "out": true,
}

// Build 对 root 下的所有 .go 文件建图。结果按 (File, Line, Name) 全序排序，
// 相同源码布局两次 Build 的 Query 结果逐字节一致。
func Build(root string) (*Index, error) {
	return BuildIncremental(root, nil)
}

// BuildIncremental 复用 prev 中 mtime+size 未变的文件解析结果，
// 只重新解析新增/改动文件；消解阶段始终基于完整定义集重跑，
// 因此增量与全量构建的 Conf 分档完全一致。prev 为 nil 等价于全量 Build。
func BuildIncremental(root string, prev *Index) (*Index, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("codegraph: resolve root %q: %w", root, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("codegraph: stat root %q: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("codegraph: %q is not a directory", root)
	}
	clean := filepath.Clean(abs)
	reuse := prev != nil && filepath.Clean(prev.root) == clean

	files := make(map[string]*fileData)
	walkErr := filepath.WalkDir(abs, func(p string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// 与 repo_map 一致：单个不可读条目不至于让整张图失败。
			return nil
		}
		if entry.IsDir() {
			if p != abs && ignoredDirs[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".go") {
			return nil
		}
		rel, err := filepath.Rel(abs, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		stamp, ok := statFile(p)
		if !ok {
			return nil
		}
		if reuse {
			if old, exists := prev.files[rel]; exists && old.stamp == stamp {
				files[rel] = old // 只读复用，后续 finalize 不会改动它
				return nil
			}
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		fd := parseGoFile(rel, src)
		fd.stamp = stamp
		files[rel] = fd
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("codegraph: walk %q: %w", root, walkErr)
	}

	ix := &Index{root: clean, files: files}
	ix.finalize()
	return ix, nil
}

// statFile 返回普通文件的 mtime(UnixNano)+size 指纹。
func statFile(path string) (fileStamp, bool) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return fileStamp{}, false
	}
	return fileStamp{modTime: info.ModTime().UnixNano(), size: info.Size()}, true
}

// finalize 拍平定义与引用边并统一消解。所有中间集合都按相对路径排序，
// 不依赖 map 迭代顺序，从而保证确定性。
func (ix *Index) finalize() {
	rels := make([]string, 0, len(ix.files))
	for rel := range ix.files {
		rels = append(rels, rel)
	}
	sort.Strings(rels)

	ix.defs = nil
	ix.byName = make(map[string][]Symbol)
	raw := make([]Edge, 0)
	for _, rel := range rels {
		fd := ix.files[rel]
		for _, sym := range fd.symbols {
			ix.defs = append(ix.defs, sym)
			ix.byName[sym.Name] = append(ix.byName[sym.Name], sym)
		}
		raw = append(raw, fd.edges...)
	}
	sort.Slice(ix.defs, func(i, j int) bool { return symLess(ix.defs[i], ix.defs[j]) })
	for name := range ix.byName {
		list := ix.byName[name]
		sort.Slice(list, func(i, j int) bool { return symLess(list[i], list[j]) })
	}

	ix.edges = make([]Edge, 0, len(raw))
	for _, e := range raw {
		e.Conf = "candidate"
		if len(ix.matchDefs(e.To)) == 1 {
			e.Conf = "exact"
		}
		ix.edges = append(ix.edges, e)
	}
	sort.Slice(ix.edges, func(i, j int) bool { return edgeLess(ix.edges[i], ix.edges[j]) })
}

// matchDefs 按名字 + 可选 pkg/receiver 限定找候选定义。
func (ix *Index) matchDefs(ref Ref) []Symbol {
	cands := ix.byName[ref.Name]
	var out []Symbol
	for _, sym := range cands {
		if ref.Pkg != "" && sym.Pkg != ref.Pkg {
			continue
		}
		if ref.Receiver != "" && sym.Receiver != ref.Receiver {
			continue
		}
		out = append(out, sym)
	}
	return out
}

// Stats 返回索引规模；nil Index 视为空图。
func (ix *Index) Stats() Stats {
	if ix == nil {
		return Stats{}
	}
	return Stats{Files: len(ix.files), Symbols: len(ix.defs), Edges: len(ix.edges)}
}

// symLess 是全序比较器：(File, Line, Name) 优先，其余字段只作稳定决胜。
func symLess(a, b Symbol) bool {
	if a.File != b.File {
		return a.File < b.File
	}
	if a.Line != b.Line {
		return a.Line < b.Line
	}
	if a.Name != b.Name {
		return a.Name < b.Name
	}
	if a.Col != b.Col {
		return a.Col < b.Col
	}
	if a.Kind != b.Kind {
		return a.Kind < b.Kind
	}
	if a.Receiver != b.Receiver {
		return a.Receiver < b.Receiver
	}
	return a.Pkg < b.Pkg
}

// edgeLess 以证据位置为首键，保证同一行源码的多条边也有稳定顺序。
func edgeLess(a, b Edge) bool {
	if a.At.File != b.At.File {
		return a.At.File < b.At.File
	}
	if a.At.Line != b.At.Line {
		return a.At.Line < b.At.Line
	}
	if a.To.Name != b.To.Name {
		return a.To.Name < b.To.Name
	}
	if a.Rule != b.Rule {
		return a.Rule < b.Rule
	}
	if a.At.Col != b.At.Col {
		return a.At.Col < b.At.Col
	}
	if a.From.Name != b.From.Name {
		return a.From.Name < b.From.Name
	}
	if a.From.Receiver != b.From.Receiver {
		return a.From.Receiver < b.From.Receiver
	}
	return a.Snippet < b.Snippet
}
