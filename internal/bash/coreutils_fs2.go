package bash

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 本文件补齐一批只依赖标准库的文件/系统查询内建命令。
// 约定：
//   - 所有触碰用户给定路径的操作都先经 resolvePath 校验工作区边界；
//   - 平台相关且标准库无法可靠完成的操作（chown/chgrp/umask）给出明确中文说明，
//     而不是静默失败；
//   - 输出格式尽量对齐 GNU coreutils（find 每行一个路径，chmod/chown 无输出等）。

// ---------------------------------------------------------------------------
// 公共小工具
// ---------------------------------------------------------------------------

// workspaceAbs 返回工作区根目录的绝对形式，供"不要删到根"这类护栏使用。
func (it *Interp) workspaceAbs() string {
	root := it.Workspace
	if root == "" {
		root = it.Cwd
	}
	if root == "" {
		root = "."
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return ""
	}
	return abs
}

// insideWorkspace 复用 resolvePath 的全部校验，报告 abs 是否仍位于工作区内。
func (it *Interp) insideWorkspace(abs string) bool {
	_, err := it.resolvePath(abs)
	return err == nil
}

// ---------------------------------------------------------------------------
// rmdir [-p] dir...
// ---------------------------------------------------------------------------

// biRmdir 删除空目录；-p 在删除叶子后继续向上删除变空的父目录。
func (it *Interp) biRmdir(args []string) (string, string, error) {
	p, err := parseShortFlags(args, "p", "")
	if err != nil {
		return it.failCmd("rmdir", 2, err)
	}
	if len(p.operands) == 0 {
		return it.failCmd("rmdir", 1, fmt.Errorf("missing operand"))
	}
	recursiveParents := hasFlag(p, 'p')
	root := it.workspaceAbs()
	for _, op := range p.operands {
		abs, err := it.resolvePath(op)
		if err != nil {
			return it.failCmd("rmdir", 1, err)
		}
		cur := abs
		first := true
		for {
			// os.Remove 既能删空目录也能删普通文件；rmdir 必须对非目录报错，
			// 否则 `rmdir somefile.txt` 会误删文件。
			info, statErr := os.Lstat(cur)
			if statErr != nil {
				if first {
					return it.failCmd("rmdir", 1, fmt.Errorf("%s: %w", op, statErr))
				}
				break
			}
			if !info.IsDir() {
				if first {
					return it.failCmd("rmdir", 1, fmt.Errorf("%s: 不是目录", op))
				}
				break
			}
			rmErr := os.Remove(cur) // 此时 cur 必是目录，非空会报错
			if rmErr != nil {
				if first {
					return it.failCmd("rmdir", 1, fmt.Errorf("%s: %w", op, rmErr))
				}
				break // 中间父目录非空：按 -p 语义静默停止
			}
			first = false
			if !recursiveParents {
				break
			}
			parent := filepath.Dir(cur)
			// 到工作区根或越界即停，绝不删除工作区本身。
			if root != "" && filepath.Clean(parent) == filepath.Clean(root) {
				break
			}
			if !it.insideWorkspace(parent) {
				break
			}
			cur = parent
		}
	}
	it.LastStatus = 0
	return "", "", nil
}

// ---------------------------------------------------------------------------
// ln [-s] [-f] target link
// ---------------------------------------------------------------------------

// biLn 创建硬链接（默认）或符号链接（-s）；-f 先删除已存在的目标链接。
func (it *Interp) biLn(args []string) (string, string, error) {
	p, err := parseShortFlags(args, "sf", "")
	if err != nil {
		return it.failCmd("ln", 2, err)
	}
	if len(p.operands) != 2 {
		return it.failCmd("ln", 1, fmt.Errorf("usage: ln [-s] [-f] target link"))
	}
	targetArg, linkArg := p.operands[0], p.operands[1]
	targetAbs, err := it.resolvePath(targetArg)
	if err != nil {
		return it.failCmd("ln", 1, err)
	}
	linkAbs, err := it.resolvePath(linkArg)
	if err != nil {
		return it.failCmd("ln", 1, err)
	}
	if hasFlag(p, 'f') {
		if _, statErr := os.Lstat(linkAbs); statErr == nil {
			if rmErr := os.Remove(linkAbs); rmErr != nil {
				return it.failCmd("ln", 1, fmt.Errorf("%s: %w", linkArg, rmErr))
			}
		}
	}
	if hasFlag(p, 's') {
		// 符号链接保存用户书写的目标文本，这样相对链接在移动后仍按 GNU 语义解析。
		if err := os.Symlink(targetArg, linkAbs); err != nil {
			return it.failCmd("ln", 1, fmt.Errorf("%s: %w", linkArg, err))
		}
	} else {
		if err := os.Link(targetAbs, linkAbs); err != nil {
			return it.failCmd("ln", 1, fmt.Errorf("%s: %w", linkArg, err))
		}
	}
	it.LastStatus = 0
	return "", "", nil
}

// ---------------------------------------------------------------------------
// chmod [-R] mode file...
// ---------------------------------------------------------------------------

// isOctalMode 报告 token 是否是纯八进制权限串。
func isOctalMode(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '7' {
			return false
		}
	}
	return true
}

// parseFileMode 把八进制或符号模式应用到当前权限 cur，返回新的权限位。
// 支持 r/w/x/X、u/g/o/a、+/-/=、逗号分隔多段，以及 s/t 特殊位。
func parseFileMode(token string, cur os.FileMode, isDir bool) (os.FileMode, error) {
	if isOctalMode(token) {
		n, err := strconv.ParseUint(token, 8, 32)
		if err != nil {
			return 0, err
		}
		v := n & 0o7777
		m := os.FileMode(v & 0o777)
		if v&0o4000 != 0 {
			m |= os.ModeSetuid
		}
		if v&0o2000 != 0 {
			m |= os.ModeSetgid
		}
		if v&0o1000 != 0 {
			m |= os.ModeSticky
		}
		return m, nil
	}
	m := cur
	for _, clause := range strings.Split(token, ",") {
		if clause == "" {
			continue
		}
		idx := strings.IndexAny(clause, "+-=")
		if idx < 0 {
			return 0, fmt.Errorf("invalid mode %q", token)
		}
		who, op, rest := clause[:idx], clause[idx], clause[idx+1:]
		hu, hg, ho := false, false, false
		if who == "" {
			hu, hg, ho = true, true, true // GNU：省略 who 等价于 a
		} else {
			for _, w := range who {
				switch w {
				case 'u':
					hu = true
				case 'g':
					hg = true
				case 'o':
					ho = true
				case 'a':
					hu, hg, ho = true, true, true
				default:
					return 0, fmt.Errorf("invalid mode %q", token)
				}
			}
		}
		var permMask os.FileMode
		if hu {
			permMask |= 0o700
		}
		if hg {
			permMask |= 0o070
		}
		if ho {
			permMask |= 0o007
		}
		var bits os.FileMode
		var special os.FileMode
		for _, r := range rest {
			switch r {
			case 'r':
				bits |= 0o444
			case 'w':
				bits |= 0o222
			case 'x':
				bits |= 0o111
			case 'X':
				if isDir || cur&0o111 != 0 {
					bits |= 0o111
				}
			case 's':
				if hu {
					special |= os.ModeSetuid
				}
				if hg {
					special |= os.ModeSetgid
				}
			case 't':
				if ho {
					special |= os.ModeSticky
				}
			default:
				return 0, fmt.Errorf("invalid mode %q", token)
			}
		}
		apply := bits & permMask
		clearSpecial := os.FileMode(0)
		if hu {
			clearSpecial |= os.ModeSetuid
		}
		if hg {
			clearSpecial |= os.ModeSetgid
		}
		if ho {
			clearSpecial |= os.ModeSticky
		}
		switch op {
		case '+':
			m |= apply
			m |= special
		case '-':
			m &^= apply
			m &^= special
		case '=':
			m = (m &^ permMask) | apply
			m &^= clearSpecial
			m |= special
		}
	}
	return m, nil
}

// collectWalkFiles 先完整遍历再返回路径列表，避免 chmod 先关掉读权限后遍历失败。
func collectWalkFiles(it *Interp, abs string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(abs, func(path string, _ os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // 跳过不可达条目，保持尽力而为
		}
		paths = append(paths, path)
		return nil
	})
	return paths, err
}

// biChmod 修改权限；-R 递归修改目录树。
func (it *Interp) biChmod(args []string) (string, string, error) {
	p, err := parseShortFlags(args, "R", "")
	if err != nil {
		return it.failCmd("chmod", 2, err)
	}
	if len(p.operands) < 2 {
		return it.failCmd("chmod", 1, fmt.Errorf("missing mode or file operand"))
	}
	modeToken := p.operands[0]
	for _, op := range p.operands[1:] {
		abs, err := it.resolvePath(op)
		if err != nil {
			return it.failCmd("chmod", 1, err)
		}
		targets := []string{abs}
		if hasFlag(p, 'R') {
			targets, err = collectWalkFiles(it, abs)
			if err != nil {
				return it.failCmd("chmod", 1, fmt.Errorf("%s: %w", op, err))
			}
		}
		for _, t := range targets {
			safe, err := it.resolvePath(t)
			if err != nil {
				return it.failCmd("chmod", 1, err)
			}
			info, err := os.Lstat(safe)
			if err != nil {
				return it.failCmd("chmod", 1, fmt.Errorf("%s: %w", op, err))
			}
			mode, err := parseFileMode(modeToken, info.Mode(), info.IsDir())
			if err != nil {
				return it.failCmd("chmod", 1, err)
			}
			if err := os.Chmod(safe, mode); err != nil {
				return it.failCmd("chmod", 1, fmt.Errorf("%s: %w", op, err))
			}
		}
	}
	it.LastStatus = 0
	return "", "", nil
}

// ---------------------------------------------------------------------------
// chown [-R] owner[:group] file... / chgrp [-R] group file...
// ---------------------------------------------------------------------------

// parseNumericID 把 ID 串解析为数字；标准库无法按名称解析用户/组，故明确告知。
func parseNumericID(cmd, kind, s string) (int, error) {
	if s == "" {
		return -1, nil // -1 表示保持该项不变
	}
	id, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%s: 无法解析%s %q：标准库不支持用户/组名称解析，请提供数字 ID（%v）", cmd, kind, s, err)
	}
	return id, nil
}

// parseOwnerSpec 解析 owner[:group]，返回 uid/gid；gid 为 -1 表示未指定。
func parseOwnerSpec(cmd, spec string) (int, int, error) {
	owner, group := spec, ""
	if i := strings.IndexByte(spec, ':'); i >= 0 {
		owner, group = spec[:i], spec[i+1:]
	}
	uid, err := parseNumericID(cmd, "用户", owner)
	if err != nil {
		return 0, 0, err
	}
	gid, err := parseNumericID(cmd, "组", group)
	if err != nil {
		return 0, 0, err
	}
	return uid, gid, nil
}

// applyChown 对解析后的路径执行 os.Chown，并对 Windows 等不支持的平台给出明确说明。
func applyChown(it *Interp, cmd, display string, uid, gid int, recursive bool) (string, string, error) {
	abs, err := it.resolvePath(display)
	if err != nil {
		return it.failCmd(cmd, 1, err)
	}
	targets := []string{abs}
	if recursive {
		targets, err = collectWalkFiles(it, abs)
		if err != nil {
			return it.failCmd(cmd, 1, fmt.Errorf("%s: %w", display, err))
		}
	}
	for _, t := range targets {
		safe, err := it.resolvePath(t)
		if err != nil {
			return it.failCmd(cmd, 1, err)
		}
		if err := os.Chown(safe, uid, gid); err != nil {
			return it.failCmd(cmd, 1, fmt.Errorf("%s: os.Chown 失败（当前平台可能不支持所有权修改）：%w", display, err))
		}
	}
	it.LastStatus = 0
	return "", "", nil
}

// biChown 修改文件所有者/所属组；Windows 上 os.Chown 通常不可用，会给出明确错误。
func (it *Interp) biChown(args []string) (string, string, error) {
	p, err := parseShortFlags(args, "R", "")
	if err != nil {
		return it.failCmd("chown", 2, err)
	}
	if len(p.operands) < 2 {
		return it.failCmd("chown", 1, fmt.Errorf("missing owner or file operand"))
	}
	uid, gid, err := parseOwnerSpec("chown", p.operands[0])
	if err != nil {
		return it.failCmd("chown", 1, err)
	}
	for _, op := range p.operands[1:] {
		out, stderr, err := applyChown(it, "chown", op, uid, gid, hasFlag(p, 'R'))
		if err != nil || stderr != "" {
			return out, stderr, err
		}
	}
	return "", "", nil
}

// biChgrp 修改文件所属组；语义与 chown 的组部分一致。
func (it *Interp) biChgrp(args []string) (string, string, error) {
	p, err := parseShortFlags(args, "R", "")
	if err != nil {
		return it.failCmd("chgrp", 2, err)
	}
	if len(p.operands) < 2 {
		return it.failCmd("chgrp", 1, fmt.Errorf("missing group or file operand"))
	}
	groupArg := p.operands[0]
	if strings.Contains(groupArg, ":") {
		return it.failCmd("chgrp", 1, fmt.Errorf("组名不能包含 ':'（要不要用 chown？）"))
	}
	gid, err := parseNumericID("chgrp", "组", groupArg)
	if err != nil {
		return it.failCmd("chgrp", 1, err)
	}
	for _, op := range p.operands[1:] {
		out, stderr, err := applyChown(it, "chgrp", op, -1, gid, hasFlag(p, 'R'))
		if err != nil || stderr != "" {
			return out, stderr, err
		}
	}
	return "", "", nil
}

// ---------------------------------------------------------------------------
// umask [mode]
// ---------------------------------------------------------------------------

// Go 标准库没有跨平台的 umask 接口；syscall.Umask 仅在 Unix 可用，而本仓库要求
// 单一文件同时能在 Windows 宿主编译、又能交叉编译到 Linux（加构建标签需拆文件）。
// 因此在包内维护 umask 值并用 os.FileMode 做权限计算，明确不改动操作系统掩码。
var (
	umaskMu    sync.Mutex
	shellUmask = os.FileMode(0o022)
)

// biUmask 查询/设置本 shell 的 umask（八进制）；不作用于进程/系统掩码。
func (it *Interp) biUmask(args []string) (string, string, error) {
	if len(args) > 1 {
		return it.failCmd("umask", 1, fmt.Errorf("usage: umask [mode]"))
	}
	umaskMu.Lock()
	defer umaskMu.Unlock()
	if len(args) == 0 {
		it.LastStatus = 0
		return fmt.Sprintf("%04o\n", uint32(shellUmask)), "", nil
	}
	if !isOctalMode(args[0]) {
		return it.failCmd("umask", 1, fmt.Errorf("invalid mode %q", args[0]))
	}
	n, err := strconv.ParseUint(args[0], 8, 32)
	if err != nil {
		return it.failCmd("umask", 1, err)
	}
	shellUmask = os.FileMode(n & 0o777)
	it.LastStatus = 0
	return "", "", nil
}

// currentUmask 返回包内维护的 umask 副本，供权限计算复用。
func currentUmask() os.FileMode {
	umaskMu.Lock()
	defer umaskMu.Unlock()
	return shellUmask
}

// ---------------------------------------------------------------------------
// find [path...] [expression]
// ---------------------------------------------------------------------------

// findTest 是 find 支持的一种测试条件。
type findTest struct {
	kind string // name / type / mtime
	arg  string
	sign int     // mtime：-1 表示 -N，0 表示 N，1 表示 +N
	days float64 // mtime 的天数
}

// splitFindArgs 把起始路径与表达式分开：首个以 '-'/'!'/'(' 开头的参数即为表达式起点。
func splitFindArgs(args []string) (paths, expr []string) {
	i := 0
	for i < len(args) && !strings.HasPrefix(args[i], "-") && args[i] != "!" && args[i] != "(" {
		paths = append(paths, args[i])
		i++
	}
	return paths, args[i:]
}

// parseFindExpr 解析 find 表达式；只实现 fail-closed 的确定性子集。
// -print 作为显式动作被接受，但因隐藏的默认打印已经覆盖其行为，不额外输出。
func parseFindExpr(expr []string) ([]findTest, error) {
	var tests []findTest
	for i := 0; i < len(expr); i++ {
		tok := expr[i]
		switch tok {
		case "-print":
			// 显式打印动作；默认打印已覆盖，这里只接受不报错。
		case "-name":
			if i+1 >= len(expr) {
				return nil, fmt.Errorf("-name requires an argument")
			}
			i++
			tests = append(tests, findTest{kind: "name", arg: expr[i]})
		case "-type":
			if i+1 >= len(expr) {
				return nil, fmt.Errorf("-type requires an argument")
			}
			i++
			if expr[i] != "f" && expr[i] != "d" {
				return nil, fmt.Errorf("unsupported -type %q（仅支持 f/d）", expr[i])
			}
			tests = append(tests, findTest{kind: "type", arg: expr[i]})
		case "-mtime":
			if i+1 >= len(expr) {
				return nil, fmt.Errorf("-mtime requires an argument")
			}
			i++
			v := expr[i]
			sign := 0
			switch {
			case strings.HasPrefix(v, "-"):
				sign, v = -1, v[1:]
			case strings.HasPrefix(v, "+"):
				sign, v = 1, v[1:]
			}
			n, atoiErr := strconv.Atoi(v)
			if atoiErr != nil {
				return nil, fmt.Errorf("invalid -mtime value %q", expr[i])
			}
			tests = append(tests, findTest{kind: "mtime", sign: sign, days: float64(n)})
		default:
			return nil, fmt.Errorf("unsupported find expression %q", tok)
		}
	}
	return tests, nil
}

// matchFindTests 对一个遍历条目求值所有测试（按 AND 连接）。
func matchFindTests(tests []findTest, path string, d os.DirEntry, now time.Time) (bool, error) {
	for _, t := range tests {
		switch t.kind {
		case "name":
			ok, err := filepath.Match(t.arg, d.Name())
			if err != nil {
				return false, err
			}
			if !ok {
				return false, nil
			}
		case "type":
			wantDir := t.arg == "d"
			if d.IsDir() != wantDir {
				return false, nil
			}
		case "mtime":
			info, err := d.Info()
			if err != nil {
				return false, err
			}
			age := now.Sub(info.ModTime()).Hours() / 24
			switch t.sign {
			case -1:
				if !(age < t.days) {
					return false, nil
				}
			case 1:
				if !(age > t.days) {
					return false, nil
				}
			default:
				if !(age >= t.days && age < t.days+1) {
					return false, nil
				}
			}
		}
	}
	return true, nil
}

// biFind 在工作区内递归查找文件；支持 -name/-type/-mtime/-print，默认打印路径。
func (it *Interp) biFind(args []string) (string, string, error) {
	paths, expr := splitFindArgs(args)
	if len(paths) == 0 {
		paths = []string{"."}
	}
	tests, err := parseFindExpr(expr)
	if err != nil {
		return it.failCmd("find", 1, err)
	}
	// 解析所有起始路径到工作区内的绝对形式：既做边界校验，也作为 WalkDir 的
	// 实际根节点。进程 CWD 可能与 shell 的 Cwd 不同，直接 WalkDir(相对路径)
	// 会遍历错目录，甚至越界枚举工作区外文件。
	resolved := make([]string, 0, len(paths))
	for _, start := range paths {
		abs, err := it.resolvePath(start)
		if err != nil {
			return it.failCmd("find", 1, err)
		}
		resolved = append(resolved, abs)
	}
	now := time.Now()
	var out []string
	for _, start := range resolved {
		walkErr := filepath.WalkDir(start, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil || d == nil {
				return nil // 跳过不可达条目
			}
			ok, err := matchFindTests(tests, path, d, now)
			if err != nil {
				return err
			}
			if ok {
				out = append(out, path)
			}
			return nil
		})
		if walkErr != nil {
			return it.failCmd("find", 1, walkErr)
		}
	}
	it.LastStatus = 0
	if len(out) == 0 {
		return "", "", nil
	}
	return strings.Join(out, "\n") + "\n", "", nil
}

// ---------------------------------------------------------------------------
// whereis cmd
// ---------------------------------------------------------------------------

// biWhereis 查询命令来源（内建/函数/别名）并在 PATH 中手动查找，绝不执行任何程序。
func (it *Interp) biWhereis(args []string) (string, string, error) {
	if len(args) == 0 {
		return it.failCmd("whereis", 1, fmt.Errorf("missing command name"))
	}
	sep := string(os.PathListSeparator)
	pathDirs := strings.Split(it.Vars["PATH"], sep)
	var out []string
	for _, name := range args {
		parts := []string{name + ":"}
		if isBuiltin(name) {
			parts = append(parts, "builtin")
		}
		if it.Funcs[name] != nil {
			parts = append(parts, "function")
		}
		if it.Aliases[name] != "" {
			parts = append(parts, "alias")
		}
		for _, dir := range pathDirs {
			if dir == "" {
				continue
			}
			candidate := filepath.Join(dir, name)
			info, err := os.Stat(candidate)
			if err == nil && !info.IsDir() {
				parts = append(parts, candidate)
			}
		}
		out = append(out, strings.Join(parts, " "))
	}
	it.LastStatus = 0
	return strings.Join(out, "\n") + "\n", "", nil
}

// ---------------------------------------------------------------------------
// locate pattern
// ---------------------------------------------------------------------------

// biLocate 无系统数据库时遍历工作区做近似子串匹配（结果不一定是系统全量文件）。
func (it *Interp) biLocate(args []string) (string, string, error) {
	if len(args) == 0 {
		return it.failCmd("locate", 1, fmt.Errorf("missing pattern"))
	}
	pattern := args[0]
	root := it.Workspace
	if root == "" {
		root = it.Cwd
	}
	if root == "" {
		root = "."
	}
	abs, err := it.resolvePath(root)
	if err != nil {
		return it.failCmd("locate", 1, err)
	}
	var out []string
	walkErr := filepath.WalkDir(abs, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d == nil {
			return nil
		}
		if strings.Contains(path, pattern) {
			out = append(out, path)
		}
		return nil
	})
	if walkErr != nil {
		return it.failCmd("locate", 1, walkErr)
	}
	it.LastStatus = 0
	stderr := "shell: locate: 近似查找（遍历工作区，无系统数据库）\n"
	if len(out) == 0 {
		return "", stderr, nil
	}
	return strings.Join(out, "\n") + "\n", stderr, nil
}

// ---------------------------------------------------------------------------
// basename / dirname
// ---------------------------------------------------------------------------

// biBasename 返回路径最后一段，必要时去掉指定后缀。
func (it *Interp) biBasename(args []string) (string, string, error) {
	if len(args) == 0 || len(args) > 2 {
		return it.failCmd("basename", 1, fmt.Errorf("usage: basename path [suffix]"))
	}
	clean := strings.TrimRight(args[0], "/\\")
	if clean == "" {
		it.LastStatus = 0
		return "/\n", "", nil
	}
	base := clean[strings.LastIndexAny(clean, "/\\")+1:]
	if len(args) == 2 && args[1] != "" && strings.HasSuffix(base, args[1]) {
		base = strings.TrimSuffix(base, args[1])
	}
	it.LastStatus = 0
	return base + "\n", "", nil
}

// biDirname 返回路径的目录部分（POSIX 风格，手工处理以统一 / 与 \ 的输出）。
func (it *Interp) biDirname(args []string) (string, string, error) {
	if len(args) != 1 {
		return it.failCmd("dirname", 1, fmt.Errorf("usage: dirname path"))
	}
	clean := strings.TrimRight(args[0], "/\\")
	if clean == "" {
		it.LastStatus = 0
		return "/\n", "", nil
	}
	i := strings.LastIndexAny(clean, "/\\")
	if i < 0 {
		it.LastStatus = 0
		return ".\n", "", nil
	}
	if i == 0 {
		it.LastStatus = 0
		return string(clean[0]) + "\n", "", nil
	}
	it.LastStatus = 0
	return clean[:i] + "\n", "", nil
}
