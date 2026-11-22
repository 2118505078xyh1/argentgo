package shell

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// shellBuiltinNames 是改变解释器状态或承载纯 shell 语义的内建命令。
var shellBuiltinNames = map[string]bool{
	"cd": true, "pwd": true, "echo": true, "export": true,
	"unset": true, "true": true, "false": true,
}

// coreutilNames 是能用标准库精确实现的 coreutils 子集。
// sed/awk/curl/find/xargs/sort 等刻意不在此列：它们在 interp 中走外部进程，
// 宁可交给真工具，也不做 glob 冒充正则之类的近似实现。
var coreutilNames = map[string]bool{
	"ls": true, "cat": true, "grep": true, "wc": true,
	"head": true, "tail": true, "mkdir": true, "rm": true,
	"printf": true, "test": true, "[": true,
	"touch": true, "cp": true, "mv": true, "tee": true,
}

// isBuiltin 分两组查询内建：shell 内建 + coreutils 子集。
func isBuiltin(name string) bool {
	return shellBuiltinNames[name] || coreutilNames[name]
}

// runBuiltin 执行内建命令，返回 (stdout, stderr, error)。
// 非零退出（如 false、grep 无匹配）通过 LastStatus 表达，不是 error。
func (it *Interp) runBuiltin(name string, args []string, stdin string) (string, string, error) {
	switch name {
	case "cd":
		return it.biCd(args)
	case "pwd":
		return it.biPwd(args)
	case "echo":
		return it.biEcho(args), "", nil
	case "export":
		return it.biExport(args)
	case "unset":
		return "", it.biUnset(args), nil
	case "true":
		it.LastStatus = 0
		return "", "", nil
	case "false":
		it.LastStatus = 1
		return "", "", nil
	}
	if coreutilNames[name] {
		return it.runCoreutil(name, args, stdin)
	}
	return "", fmt.Sprintf("shell: %s: command not found\n", name), nil
}

// resolvePath 把路径解析到工作区（Cwd）内，拒绝逃逸。所有文件类内建都走它。
func (it *Interp) resolvePath(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("empty path")
	}
	base := it.Cwd
	if base == "" {
		base = "."
	}
	if !isAbs(p) {
		p = filepath.Join(base, p)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	baseAbs, err := filepath.Abs(base)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(baseAbs, abs)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside the workspace", p)
	}
	// 真实路径校验：解析符号链接/junction，阻断 .. 词法检查之外的逃逸。
	// 这是 shell 与 security 的自然结合点——路径边界由 security 统一定义。
	if err := security.CheckPathInRoots(abs, []string{baseAbs}); err != nil {
		return "", fmt.Errorf("path %q is outside the workspace: %w", p, err)
	}
	return abs, nil
}

func (it *Interp) biCd(args []string) (string, string, error) {
	target := it.Cwd
	if len(args) > 0 {
		target = args[0]
	}
	abs, err := it.resolvePath(target)
	if err != nil {
		it.LastStatus = 1
		return "", fmt.Sprintf("shell: cd: %v\n", err), nil
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		it.LastStatus = 1
		return "", fmt.Sprintf("shell: cd: not a directory: %s\n", target), nil
	}
	it.Cwd = abs
	it.Vars["PWD"] = abs
	it.LastStatus = 0
	return "", "", nil
}

func (it *Interp) biPwd(args []string) (string, string, error) {
	it.LastStatus = 0
	return it.Cwd + "\n", "", nil
}

func (it *Interp) biEcho(args []string) string {
	// 简化：-n 抑制尾随换行（不做 -e 转义，避免语义膨胀）。
	noNewline := false
	if len(args) > 0 && args[0] == "-n" {
		noNewline = true
		args = args[1:]
	}
	s := strings.Join(args, " ")
	it.LastStatus = 0
	if noNewline {
		return s
	}
	return s + "\n"
}

func (it *Interp) biExport(args []string) (string, string, error) {
	for _, a := range args {
		if i := strings.IndexByte(a, '='); i > 0 {
			it.Vars[a[:i]] = a[i+1:]
		} else {
			it.Vars[a] = ""
		}
	}
	it.LastStatus = 0
	return "", "", nil
}

func (it *Interp) biUnset(args []string) string {
	for _, a := range args {
		delete(it.Vars, a)
	}
	it.LastStatus = 0
	return ""
}
