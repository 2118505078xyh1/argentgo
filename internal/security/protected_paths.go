package security

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// DefaultProtectedNames 是即使在可写根目录内也保持只读的子路径名。它们存放
// git 钩子/对象元数据（.git）、agent 状态（.argent）、云凭据（.aws）和私钥
// （.ssh）：写入其中之一会把普通的工作区写入变成持久化或权限变更。匹配按完整
// 路径段进行，因此 .github 中的文件不受影响。
var DefaultProtectedNames = []string{".git", ".argent", ".aws", ".ssh"}

// maxGitPointerBytes 限制对 .git 指针文件的尽力读取，使损坏或恶意文件不会让
// 策略评估变得昂贵。
const maxGitPointerBytes = 4096

// IsPathProtected 报告 path 是否落在 root 的受保护子路径下。path 可以是绝对路径
// 或相对于 root 的路径；返回的原因是一条简短、面向操作员的说明，只有可写路径
// 才为空。
//
// .git *文件*也会被跟随：git 用文件表示 worktree 和子模块，其 gitdir: 目标
// 正是钩子所在之处。该目标常常位于 root 之外，因此指针检查刻意运行在逃逸检查
// 之前，为针对它的写入给出更具体的原因。
//
// 词法路径和它在磁盘上的真实位置都会被检查，因此根目录内的符号链接或 Windows
// junction 无法把对受保护子路径的写入伪装成普通的工作区写入。
func IsPathProtected(root, path string) (bool, string) {
	rootAbs := resolveAgainst(root, "")
	pathAbs := resolveAgainst(path, rootAbs)

	if protected, reason := protectedWithin(rootAbs, pathAbs); protected {
		return true, reason
	}
	// 根目录*内部*的链接可以指向受保护子路径，而自身的词法写法无害
	// （例如 <root>/alias -> <root>/.ssh）。上面的词法检查看不到这一点，因此
	// 解析两侧并在真实磁盘位置上重复检查。没有这一步，一个 junction 就足以把
	// 对 .git/.ssh 的写入变成普通的工作区写入。解析失败交由 InWriteRoots 和
	// Policy.checkPath 中失败即拒绝的边界检查处理；它不会在这里扩大保护范围。
	realRoot, rootErr := ResolveRealPath(rootAbs)
	realPath, pathErr := ResolveRealPath(pathAbs)
	if rootErr == nil && pathErr == nil {
		if protected, reason := protectedWithin(realRoot, realPath); protected {
			return true, reason
		}
	}
	return false, ""
}

// protectedWithin 是对一个 (root, path) 对进行的整段检查。.git 指针细化先运行，
// 这样 worktree 的 gitdir 即使位于 root 之外也能得到具体原因；root 之外的路径
// 同样保持受保护。它是上面词法检查和真实位置检查共用的主体。
func protectedWithin(rootAbs, pathAbs string) (bool, string) {
	if gitdir, ok := gitdirPointer(rootAbs); ok && pathWithin(gitdir, pathAbs) {
		return true, "protected subpath .git (worktree gitdir)"
	}
	rel, ok := relativeSegments(rootAbs, pathAbs)
	if !ok {
		return true, "outside root"
	}
	for _, segment := range rel {
		for _, name := range DefaultProtectedNames {
			if segmentsEqual(segment, name) {
				return true, "protected subpath " + name
			}
		}
	}
	return false, ""
}

// ProtectedSubpaths 列出直接位于 root 下的受保护具体绝对路径：每个受保护名字
// 一个，加上 root/.git 为指针文件时解析出的 gitdir。它用于测试和自省；权威检查
// 是 IsPathProtected，它还能捕获嵌套的受保护段。
func ProtectedSubpaths(root string) []string {
	rootAbs := resolveAgainst(root, "")
	out := make([]string, 0, len(DefaultProtectedNames)+1)
	for _, name := range DefaultProtectedNames {
		out = append(out, filepath.Join(rootAbs, name))
	}
	if gitdir, ok := gitdirPointer(rootAbs); ok {
		out = append(out, gitdir)
	}
	return out
}

// protectedInWriteRoots 是 Policy.checkWriteRoots 使用的桥接函数。路径会针对
// 实际包含它的根目录进行检查，因此额外写入根目录中的路径不会被错误报告为
// 工作区根目录"之外"。
func protectedInWriteRoots(path string, roots []string) (bool, string) {
	base := ""
	if len(roots) > 0 && !filepath.IsAbs(expandHome(strings.TrimSpace(path))) {
		base = roots[0]
	}
	candidate := resolveAgainst(path, base)
	for _, root := range roots {
		rootAbs := resolveAgainst(root, "")
		if pathWithin(rootAbs, candidate) {
			return IsPathProtected(rootAbs, candidate)
		}
	}
	return false, ""
}

// resolveAgainst 使 path 成为干净的绝对路径。给出 base 时，相对路径会拼接到
// base 上，否则拼接到当前目录。
func resolveAgainst(path, base string) string {
	candidate := expandHome(strings.TrimSpace(path))
	if candidate == "" {
		candidate = "."
	}
	if !filepath.IsAbs(candidate) && base != "" {
		candidate = filepath.Join(base, candidate)
	}
	if absolute, err := filepath.Abs(candidate); err == nil {
		candidate = absolute
	}
	return filepath.Clean(candidate)
}

// 当 <root>/.git 是一个小的普通文件时，gitdirPointer 读取它并返回其 "gitdir:"
// 行声明的绝对 git 目录。错误会被忽略：这是对边界检查的尽力细化，该检查在其他
// 地方失败即拒绝。大小上限使读取保持有界。
func gitdirPointer(root string) (string, bool) {
	dotGit := filepath.Join(root, ".git")
	info, err := os.Stat(dotGit)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxGitPointerBytes {
		return "", false
	}
	data, err := os.ReadFile(dotGit)
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(strings.ToLower(line), "gitdir:") {
			continue
		}
		target := strings.TrimSpace(line[len("gitdir:"):])
		if target == "" {
			return "", false
		}
		return resolveAgainst(target, root), true
	}
	return "", false
}

// pathWithin 报告 candidate 是 base 本身还是位于其下。
func pathWithin(base, candidate string) bool {
	_, ok := relativeSegments(base, candidate)
	return ok
}

// relativeSegments 返回 candidate 在 base 之下的各段。当 candidate 位于 base
// 之外（包括位于不同的 Windows 卷上）时 ok 为 false。
func relativeSegments(base, candidate string) ([]string, bool) {
	baseSegs := pathSegments(base)
	candSegs := pathSegments(candidate)
	if len(candSegs) < len(baseSegs) {
		return nil, false
	}
	for i, segment := range baseSegs {
		if !segmentsEqual(segment, candSegs[i]) {
			return nil, false
		}
	}
	return candSegs[len(baseSegs):], true
}

// pathSegments 把清理后的路径拆分成可比较的段，丢弃根目录标记和 "." 项。
func pathSegments(path string) []string {
	parts := strings.Split(filepath.Clean(path), string(filepath.Separator))
	segments := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" && part != "." {
			segments = append(segments, part)
		}
	}
	return segments
}

// segmentsEqual 在 Windows 上（文件系统如此）不区分大小写，其他地方精确匹配，
// 因此 Linux 上的 .GIT 目录是一个不同的名字。
func segmentsEqual(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
