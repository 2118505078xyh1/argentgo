package shell

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// 内建命令分派。coreutils（ls/cat/...）用标准库实现，使无系统 shell/无 GNU
// 工具时命令仍可用（dsh webworker 的命令表思想）。
func isBuiltin(name string) bool {
	switch name {
	case "cd", "pwd", "echo", "export", "unset", "true", "false",
		"ls", "cat", "grep", "wc", "head", "tail", "mkdir", "rm":
		return true
	}
	return false
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
	case "ls":
		return it.biLs(args)
	case "cat":
		return it.biCat(args, stdin)
	case "grep":
		return it.biGrep(args, stdin)
	case "wc":
		return it.biWc(args, stdin)
	case "head":
		return it.biHead(args, stdin)
	case "tail":
		return it.biTail(args, stdin)
	case "mkdir":
		return it.biMkdir(args)
	case "rm":
		return it.biRm(args)
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

func (it *Interp) biLs(args []string) (string, string, error) {
	dir := "."
	var names []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue // 忽略标志（简化）
		}
		if names == nil {
			dir = a
		} else {
			names = append(names, a)
		}
		if names == nil {
			names = []string{}
		}
	}
	abs, err := it.resolvePath(dir)
	if err != nil {
		it.LastStatus = 1
		return "", fmt.Sprintf("shell: ls: %v\n", err), nil
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		it.LastStatus = 1
		return "", fmt.Sprintf("shell: ls: %v\n", err), nil
	}
	var lines []string
	for _, e := range entries {
		if len(names) > 0 {
			match := false
			for _, n := range names {
				if ok, _ := filepath.Match(n, e.Name()); ok {
					match = true
				}
			}
			if !match {
				continue
			}
		}
		lines = append(lines, e.Name())
	}
	sort.Strings(lines)
	it.LastStatus = 0
	if len(lines) == 0 {
		return "", "", nil
	}
	return strings.Join(lines, "\n") + "\n", "", nil
}

func (it *Interp) biCat(args []string, stdin string) (string, string, error) {
	if len(args) == 0 {
		it.LastStatus = 0
		return stdin, "", nil
	}
	var b strings.Builder
	for _, a := range args {
		abs, err := it.resolvePath(a)
		if err != nil {
			it.LastStatus = 1
			return "", fmt.Sprintf("shell: cat: %v\n", err), nil
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			it.LastStatus = 1
			return "", fmt.Sprintf("shell: cat: %v\n", err), nil
		}
		b.Write(data)
	}
	it.LastStatus = 0
	return b.String(), "", nil
}

func (it *Interp) biGrep(args []string, stdin string) (string, string, error) {
	pattern := ""
	var files []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		if pattern == "" {
			pattern = a
		} else {
			files = append(files, a)
		}
	}
	if pattern == "" {
		it.LastStatus = 2
		return "", "shell: grep: missing pattern\n", nil
	}
	input := stdin
	if len(files) > 0 {
		var b strings.Builder
		for _, f := range files {
			abs, err := it.resolvePath(f)
			if err != nil {
				it.LastStatus = 1
				return "", fmt.Sprintf("shell: grep: %v\n", err), nil
			}
			data, err := os.ReadFile(abs)
			if err != nil {
				it.LastStatus = 1
				return "", fmt.Sprintf("shell: grep: %v\n", err), nil
			}
			b.Write(data)
		}
		input = b.String()
	}
	var matched []string
	for _, line := range strings.Split(input, "\n") {
		if ok, err := simpleGlobMatch(pattern, line); err == nil && ok {
			matched = append(matched, line)
		}
	}
	it.LastStatus = 0
	if len(matched) == 0 {
		it.LastStatus = 1
		return "", "", nil
	}
	return strings.Join(matched, "\n") + "\n", "", nil
}

// simpleGlobMatch 用 filepath.Match 做 grep 模式匹配（简化的 glob 风格而非正则）。
func simpleGlobMatch(pattern, s string) (bool, error) {
	return filepath.Match("*"+pattern+"*", s)
}

func (it *Interp) biWc(args []string, stdin string) (string, string, error) {
	input := stdin
	if len(args) > 0 {
		abs, err := it.resolvePath(args[0])
		if err != nil {
			it.LastStatus = 1
			return "", fmt.Sprintf("shell: wc: %v\n", err), nil
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			it.LastStatus = 1
			return "", fmt.Sprintf("shell: wc: %v\n", err), nil
		}
		input = string(data)
	}
	lines := len(strings.Split(strings.TrimRight(input, "\n"), "\n"))
	if input == "" {
		lines = 0
	}
	words := len(strings.Fields(input))
	chars := len([]rune(input))
	it.LastStatus = 0
	return fmt.Sprintf("%d %d %d\n", lines, words, chars), "", nil
}

func (it *Interp) biHead(args []string, stdin string) (string, string, error) {
	n := 10
	var fileArg string
	for i := 0; i < len(args); i++ {
		if args[i] == "-n" && i+1 < len(args) {
			fmt.Sscanf(args[i+1], "%d", &n)
			i++
		} else if !strings.HasPrefix(args[i], "-") {
			fileArg = args[i]
		}
	}
	input := stdin
	if fileArg != "" {
		abs, err := it.resolvePath(fileArg)
		if err != nil {
			it.LastStatus = 1
			return "", fmt.Sprintf("shell: head: %v\n", err), nil
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			it.LastStatus = 1
			return "", fmt.Sprintf("shell: head: %v\n", err), nil
		}
		input = string(data)
	}
	lines := strings.Split(input, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	it.LastStatus = 0
	return strings.Join(lines, "\n"), "", nil
}

func (it *Interp) biTail(args []string, stdin string) (string, string, error) {
	n := 10
	var fileArg string
	for i := 0; i < len(args); i++ {
		if args[i] == "-n" && i+1 < len(args) {
			fmt.Sscanf(args[i+1], "%d", &n)
			i++
		} else if !strings.HasPrefix(args[i], "-") {
			fileArg = args[i]
		}
	}
	input := stdin
	if fileArg != "" {
		abs, err := it.resolvePath(fileArg)
		if err != nil {
			it.LastStatus = 1
			return "", fmt.Sprintf("shell: tail: %v\n", err), nil
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			it.LastStatus = 1
			return "", fmt.Sprintf("shell: tail: %v\n", err), nil
		}
		input = string(data)
	}
	lines := strings.Split(strings.TrimRight(input, "\n"), "\n")
	if input == "" {
		lines = nil
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	it.LastStatus = 0
	if len(lines) == 0 {
		return "", "", nil
	}
	return strings.Join(lines, "\n") + "\n", "", nil
}

func (it *Interp) biMkdir(args []string) (string, string, error) {
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		abs, err := it.resolvePath(a)
		if err != nil {
			it.LastStatus = 1
			return "", fmt.Sprintf("shell: mkdir: %v\n", err), nil
		}
		if err := os.MkdirAll(abs, 0o755); err != nil {
			it.LastStatus = 1
			return "", fmt.Sprintf("shell: mkdir: %v\n", err), nil
		}
	}
	it.LastStatus = 0
	return "", "", nil
}

func (it *Interp) biRm(args []string) (string, string, error) {
	recursive := false
	for _, a := range args {
		if a == "-r" || a == "-rf" || a == "-fr" {
			recursive = true
		}
	}
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		abs, err := it.resolvePath(a)
		if err != nil {
			it.LastStatus = 1
			return "", fmt.Sprintf("shell: rm: %v\n", err), nil
		}
		if recursive {
			err = os.RemoveAll(abs)
		} else {
			err = os.Remove(abs)
		}
		if err != nil {
			it.LastStatus = 1
			return "", fmt.Sprintf("shell: rm: %v\n", err), nil
		}
	}
	it.LastStatus = 0
	return "", "", nil
}
