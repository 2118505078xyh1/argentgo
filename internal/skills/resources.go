package skills

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// 排序必须按路径确定。
// ResourceInfo 描述随技能 SKILL.md 一起发布的辅助文件（例如 scripts/ 下的脚本或 references/ 下的文档）。Path 是相对于技能
// references/ 下的文档）。Path 是相对于技能目录的斜杠分隔路径；
// 目录的斜杠分隔路径；Size 是文件字节大小。调用方用此清单告诉模型在技能加载后可以读取或运行哪些文件。
// 供模型在技能加载后读取或运行哪些文件。
type ResourceInfo struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// ListSkillResources 列出 skillDir 下的辅助文件。它是无依赖、无全局状态的只读扫描：结果仅取决于目录内容和 maxBytes。
// 它是无依赖、无全局状态的只读扫描：结果
// 仅取决于目录内容和 maxBytes。
//
// Rules:
//
//   - 技能文档本身（SKILL.md 或 SKILL.markdown，不区分大小写，任意深度）不是资源，被省略；
//
// 任意深度）不是资源，被省略；
//   - 隐藏文件和目录（以 '.' 开头）被跳过，且
//   - 隐藏文件和目录（以 '.' 开头）被跳过，且不进入隐藏目录；
//   - 解析到 skillDir 之外的符号链接被跳过，指向非常规文件的符号链接也被跳过。停留在内部的符号链接按其自身相对路径列出，大小为其目标的大小；
//   - 指向非常规文件的符号链接也被跳过。停留在内部的符号链接按其自身相对路径列出，
//     大小为其目标的大小；
//   - 只列出普通文件；
//   - 结果按 Path 排序；
//   - maxBytes 是总字节预算。文件按路径顺序累加，
//     任何会超出剩余预算的文件被跳过，这也
//   - maxBytes 是总字节预算。文件按路径顺序累加，任何会超出剩余预算的文件被跳过，这也排除了任何大于 maxBytes 的单个文件。非正数
//     maxBytes 不产生资源。
//
// 不可读的条目被跳过，而非使整个扫描失败。
func ListSkillResources(skillDir string, maxBytes int64) []ResourceInfo {
	if maxBytes <= 0 {
		return nil
	}
	root, err := filepath.Abs(skillDir)
	if err != nil {
		return nil
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return nil
	}

	type candidate struct {
		path string
		size int64
	}
	var candidates []candidate

	filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil || rel == "." {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if isSkillDocument(name) {
			return nil
		}
		size, ok := resourceSize(p, d, root)
		if !ok {
			return nil
		}
		candidates = append(candidates, candidate{path: filepath.ToSlash(rel), size: size})
		return nil
	})

	if len(candidates) == 0 {
		return nil
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].path < candidates[j].path })

	var out []ResourceInfo
	var total int64
	for _, c := range candidates {
		if c.size < 0 || c.size > maxBytes-total {
			continue
		}
		total += c.size
		out = append(out, ResourceInfo{Path: c.path, Size: c.size})
	}
	return out
}

// isSkillDocument 报告 name 是否为技能文档而非资源。匹配不区分大小写，使 Windows 上大小写不同的重复项不会泄漏进清单。
// 资源。匹配不区分大小写，使 Windows 上大小写不同的
// 重复项不会泄漏进清单。
func isSkillDocument(name string) bool {
	lower := strings.ToLower(name)
	return lower == "skill.md" || lower == "skill.markdown"
}

// resourceSize 返回 p 所代表的普通文件的大小。符号链接被解析；逃逸 root 或指向非常规文件的被拒绝，停留在内部的按其目标大小报告。
// 符号链接被解析；逃逸 root 或指向非常规文件的被拒绝，
// 停留在内部的按其目标大小报告。
func resourceSize(p string, d fs.DirEntry, root string) (int64, bool) {
	if d.Type()&os.ModeSymlink != 0 {
		resolved, err := filepath.EvalSymlinks(p)
		if err != nil || !pathWithin(root, resolved) {
			return 0, false
		}
		info, err := os.Stat(resolved)
		if err != nil || !info.Mode().IsRegular() {
			return 0, false
		}
		return info.Size(), true
	}
	info, err := d.Info()
	if err != nil || !info.Mode().IsRegular() {
		return 0, false
	}
	return info.Size(), true
}

// pathWithin 报告 target 是否为 root 本身或其下的路径。它是拒绝逃逸技能目录的符号链接的包含性检查。
// 它是拒绝逃逸技能目录的符号链接的包含性检查。
func pathWithin(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." &&
		!strings.HasPrefix(rel, ".."+string(filepath.Separator)) &&
		!filepath.IsAbs(rel)
}
