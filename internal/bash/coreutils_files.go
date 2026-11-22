package bash

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// lsEntry 是 ls 排序与长格式所需的条目信息。
type lsEntry struct {
	name    string
	mode    os.FileMode
	size    int64
	mod     time.Time
	isDir   bool
	symlink bool
}

func (it *Interp) biLs(args []string) (string, string, error) {
	p, err := parseShortFlags(args, "laR1d", "")
	if err != nil {
		return it.failCmd("ls", 2, err)
	}
	all := hasFlag(p, 'a')
	long := hasFlag(p, 'l')
	recursive := hasFlag(p, 'R')
	dirsOnly := hasFlag(p, 'd')
	_ = hasFlag(p, '1') // 默认非终端输出已是一行一项

	operands := p.operands
	if len(operands) == 0 {
		operands = []string{"."}
	}
	var out []string
	for i, op := range operands {
		if i > 0 {
			out = append(out, "")
		}
		abs, err := it.resolvePath(op)
		if err != nil {
			return it.failCmd("ls", 1, err)
		}
		info, err := os.Stat(abs)
		if err != nil {
			return it.failCmd("ls", 1, fmt.Errorf("%s: %w", op, err))
		}
		if dirsOnly || !info.IsDir() {
			if long && !dirsOnly {
				e := lsEntry{name: op, mode: info.Mode(), size: info.Size(), mod: info.ModTime(), isDir: info.IsDir()}
				out = append(out, lsFormatLong(e, len(strconv.FormatInt(info.Size(), 10))))
			} else {
				out = append(out, op)
			}
			continue
		}
		header := len(operands) > 1 || recursive
		if err := it.lsWalk(abs, op, header, all, long, recursive, &out); err != nil {
			return it.failCmd("ls", 1, err)
		}
	}
	it.LastStatus = 0
	if len(out) == 0 {
		return "", "", nil
	}
	return strings.Join(out, "\n") + "\n", "", nil
}

// lsWalk 列一个目录；-R 时递归到子目录并在每组前打印 "dir:" 头。
func (it *Interp) lsWalk(dirAbs, label string, header, all, long, recursive bool, out *[]string) error {
	entries, err := it.lsReadEntries(dirAbs, all)
	if err != nil {
		return err
	}
	if header {
		*out = append(*out, label+":")
	}
	width := 0
	if long {
		for _, e := range entries {
			if w := len(strconv.FormatInt(e.size, 10)); w > width {
				width = w
			}
		}
	}
	for _, e := range entries {
		if long {
			*out = append(*out, lsFormatLong(e, width))
		} else {
			*out = append(*out, e.name)
		}
	}
	if recursive {
		for _, e := range entries {
			// 不递归 "."/".."（否则 -aR 会无限递归），也不跟随符号链接。
			if !e.isDir || e.symlink || e.name == "." || e.name == ".." {
				continue
			}
			childAbs := filepath.Join(dirAbs, e.name)
			if _, err := it.resolvePath(childAbs); err != nil {
				return err
			}
			*out = append(*out, "")
			childLabel := label + string(filepath.Separator) + e.name
			if label == "." {
				childLabel = "./" + e.name
			}
			if err := it.lsWalk(childAbs, childLabel, true, all, long, recursive, out); err != nil {
				return err
			}
		}
	}
	return nil
}

func (it *Interp) lsReadEntries(dirAbs string, all bool) ([]lsEntry, error) {
	des, err := os.ReadDir(dirAbs)
	if err != nil {
		return nil, err
	}
	var entries []lsEntry
	for _, de := range des {
		name := de.Name()
		if !all && strings.HasPrefix(name, ".") {
			continue
		}
		info, err := de.Info()
		if err != nil {
			continue
		}
		entries = append(entries, lsEntry{
			name: name, mode: info.Mode(), size: info.Size(), mod: info.ModTime(),
			isDir: info.IsDir(), symlink: info.Mode()&os.ModeSymlink != 0,
		})
	}
	if all {
		if info, err := os.Stat(dirAbs); err == nil {
			entries = append(entries, lsEntry{name: ".", mode: info.Mode(), size: info.Size(),
				mod: info.ModTime(), isDir: true})
		}
		if info, err := os.Stat(filepath.Dir(dirAbs)); err == nil {
			entries = append(entries, lsEntry{name: "..", mode: info.Mode(), size: info.Size(),
				mod: info.ModTime(), isDir: true})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	return entries, nil
}

// lsFormatLong 模拟 GNU ls -l。所有者/组用 "-" 占位、无 total 行，详见报告的偏差表。
func lsFormatLong(e lsEntry, width int) string {
	mode := e.mode.String()
	if strings.HasPrefix(mode, "L") {
		mode = "l" + mode[1:]
	}
	size := strconv.FormatInt(e.size, 10)
	if width > len(size) {
		size = strings.Repeat(" ", width-len(size)) + size
	}
	return fmt.Sprintf("%s 1 - - %s %s %s", mode, size, e.mod.Format("2006-01-02 15:04"), e.name)
}

func (it *Interp) biCat(args []string, stdin string) (string, string, error) {
	p, err := parseShortFlags(args, "", "")
	if err != nil {
		return it.failCmd("cat", 2, err)
	}
	if len(p.operands) == 0 {
		it.LastStatus = 0
		return stdin, "", nil
	}
	var b strings.Builder
	for _, op := range p.operands {
		if op == "-" {
			b.WriteString(stdin)
			continue
		}
		s, err := it.readFileArg(op, "cat")
		if err != nil {
			return it.failCmd("cat", 1, err)
		}
		b.WriteString(s)
	}
	it.LastStatus = 0
	return b.String(), "", nil
}

func (it *Interp) biMkdir(args []string) (string, string, error) {
	p, err := parseShortFlags(args, "p", "")
	if err != nil {
		return it.failCmd("mkdir", 2, err)
	}
	if len(p.operands) == 0 {
		return it.failCmd("mkdir", 1, fmt.Errorf("missing operand"))
	}
	for _, op := range p.operands {
		abs, err := it.resolvePath(op)
		if err != nil {
			return it.failCmd("mkdir", 1, err)
		}
		if hasFlag(p, 'p') {
			err = os.MkdirAll(abs, 0o755)
		} else {
			err = os.Mkdir(abs, 0o755) // 无 -p 时父目录缺失应报错（对齐 GNU）
		}
		if err != nil {
			return it.failCmd("mkdir", 1, fmt.Errorf("%s: %w", op, err))
		}
	}
	it.LastStatus = 0
	return "", "", nil
}

func (it *Interp) biRm(args []string) (string, string, error) {
	p, err := parseShortFlags(args, "rfRv", "")
	if err != nil {
		return it.failCmd("rm", 2, err)
	}
	if len(p.operands) == 0 {
		return it.failCmd("rm", 1, fmt.Errorf("missing operand"))
	}
	recursive := hasFlag(p, 'r') || hasFlag(p, 'R')
	force := hasFlag(p, 'f')
	verbose := hasFlag(p, 'v')
	base := it.Cwd
	if base == "" {
		base = "."
	}
	baseAbs, _ := filepath.Abs(base)
	var out []string
	for _, op := range p.operands {
		abs, err := it.resolvePath(op) // 逃逸一律拒绝，-f 也不能豁免
		if err != nil {
			return it.failCmd("rm", 1, err)
		}
		if recursive && filepath.Clean(abs) == filepath.Clean(baseAbs) {
			return it.failCmd("rm", 1, fmt.Errorf("refuse to remove the workspace root"))
		}
		info, statErr := os.Lstat(abs)
		if statErr != nil {
			if force && os.IsNotExist(statErr) {
				continue
			}
			return it.failCmd("rm", 1, fmt.Errorf("%s: %w", op, statErr))
		}
		if !recursive && info.IsDir() {
			return it.failCmd("rm", 1, fmt.Errorf("cannot remove %q: is a directory", op))
		}
		if recursive {
			err = os.RemoveAll(abs)
		} else {
			err = os.Remove(abs)
		}
		if err != nil && !(force && os.IsNotExist(err)) {
			return it.failCmd("rm", 1, fmt.Errorf("%s: %w", op, err))
		}
		if verbose {
			out = append(out, fmt.Sprintf("removed %q", op))
		}
	}
	it.LastStatus = 0
	if len(out) == 0 {
		return "", "", nil
	}
	return strings.Join(out, "\n") + "\n", "", nil
}

func (it *Interp) biTouch(args []string) (string, string, error) {
	p, err := parseShortFlags(args, "c", "")
	if err != nil {
		return it.failCmd("touch", 2, err)
	}
	if len(p.operands) == 0 {
		return it.failCmd("touch", 1, fmt.Errorf("missing file operand"))
	}
	now := time.Now()
	for _, op := range p.operands {
		abs, err := it.resolvePath(op)
		if err != nil {
			return it.failCmd("touch", 1, err)
		}
		if _, err := os.Stat(abs); err != nil {
			if hasFlag(p, 'c') {
				continue // GNU touch -c：不存在也不创建，且不报错
			}
			if !os.IsNotExist(err) {
				return it.failCmd("touch", 1, fmt.Errorf("%s: %w", op, err))
			}
			f, err := os.OpenFile(abs, os.O_CREATE|os.O_WRONLY, 0o644)
			if err != nil {
				return it.failCmd("touch", 1, fmt.Errorf("%s: %w", op, err))
			}
			if err := f.Close(); err != nil {
				return it.failCmd("touch", 1, err)
			}
			continue
		}
		if err := os.Chtimes(abs, now, now); err != nil {
			return it.failCmd("touch", 1, fmt.Errorf("%s: %w", op, err))
		}
	}
	it.LastStatus = 0
	return "", "", nil
}

func (it *Interp) biCp(args []string) (string, string, error) {
	p, err := parseShortFlags(args, "rfR", "")
	if err != nil {
		return it.failCmd("cp", 2, err)
	}
	if len(p.operands) < 2 {
		return it.failCmd("cp", 1, fmt.Errorf("missing source or destination operand"))
	}
	recursive := hasFlag(p, 'r') || hasFlag(p, 'R')
	_ = hasFlag(p, 'f') // GNU cp 默认即覆盖，-f 不改变输出
	sources := p.operands[:len(p.operands)-1]
	dst := p.operands[len(p.operands)-1]
	dstAbs, err := it.resolvePath(dst)
	if err != nil {
		return it.failCmd("cp", 1, err)
	}
	dstInfo, statErr := os.Stat(dstAbs)
	dstIsDir := statErr == nil && dstInfo.IsDir()
	if !dstIsDir && len(sources) > 1 {
		return it.failCmd("cp", 1, fmt.Errorf("target %q is not a directory", dst))
	}
	for _, src := range sources {
		srcAbs, err := it.resolvePath(src)
		if err != nil {
			return it.failCmd("cp", 1, err)
		}
		srcInfo, err := os.Lstat(srcAbs)
		if err != nil {
			return it.failCmd("cp", 1, fmt.Errorf("%s: %w", src, err))
		}
		target := dstAbs
		if dstIsDir {
			target = filepath.Join(dstAbs, filepath.Base(srcAbs))
		}
		targetAbs, err := it.resolvePath(target) // 目标同样过边界
		if err != nil {
			return it.failCmd("cp", 1, err)
		}
		if srcInfo.IsDir() {
			if !recursive {
				return it.failCmd("cp", 1, fmt.Errorf("omitting directory %q", src))
			}
			if err := it.copyTree(srcAbs, targetAbs); err != nil {
				return it.failCmd("cp", 1, err)
			}
			continue
		}
		if err := copyFile(srcAbs, targetAbs, srcInfo.Mode().Perm()); err != nil {
			return it.failCmd("cp", 1, fmt.Errorf("%s: %w", src, err))
		}
	}
	it.LastStatus = 0
	return "", "", nil
}

// copyTree 递归复制目录树；每个源/目标路径都再经 resolvePath 校验。
func (it *Interp) copyTree(srcAbs, dstAbs string) error {
	return filepath.Walk(srcAbs, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcAbs, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dstAbs, rel)
		if _, err := it.resolvePath(path); err != nil {
			return err
		}
		if _, err := it.resolvePath(target); err != nil {
			return err
		}
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		}
		return copyFile(path, target, info.Mode().Perm())
	})
}

func copyFile(srcAbs, dstAbs string, mode os.FileMode) error {
	in, err := os.Open(srcAbs)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dstAbs, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func (it *Interp) biMv(args []string) (string, string, error) {
	p, err := parseShortFlags(args, "fn", "")
	if err != nil {
		return it.failCmd("mv", 2, err)
	}
	if len(p.operands) < 2 {
		return it.failCmd("mv", 1, fmt.Errorf("missing source or destination operand"))
	}
	noClobber := hasFlag(p, 'n')
	sources := p.operands[:len(p.operands)-1]
	dst := p.operands[len(p.operands)-1]
	dstAbs, err := it.resolvePath(dst)
	if err != nil {
		return it.failCmd("mv", 1, err)
	}
	dstInfo, statErr := os.Stat(dstAbs)
	dstIsDir := statErr == nil && dstInfo.IsDir()
	if !dstIsDir && len(sources) > 1 {
		return it.failCmd("mv", 1, fmt.Errorf("target %q is not a directory", dst))
	}
	for _, src := range sources {
		srcAbs, err := it.resolvePath(src)
		if err != nil {
			return it.failCmd("mv", 1, err)
		}
		target := dstAbs
		if dstIsDir {
			target = filepath.Join(dstAbs, filepath.Base(srcAbs))
		}
		targetAbs, err := it.resolvePath(target)
		if err != nil {
			return it.failCmd("mv", 1, err)
		}
		if noClobber {
			if _, err := os.Stat(targetAbs); err == nil {
				continue
			}
		}
		if filepath.Clean(srcAbs) == filepath.Clean(targetAbs) {
			continue
		}
		if err := os.Rename(srcAbs, targetAbs); err != nil {
			// 跨设备 rename 失败时回退到复制 + 删除，仍走同一套边界校验。
			info, statErr := os.Lstat(srcAbs)
			if statErr != nil {
				return it.failCmd("mv", 1, fmt.Errorf("%s: %w", src, statErr))
			}
			if info.IsDir() {
				if err := it.copyTree(srcAbs, targetAbs); err != nil {
					return it.failCmd("mv", 1, err)
				}
			} else if err := copyFile(srcAbs, targetAbs, info.Mode().Perm()); err != nil {
				return it.failCmd("mv", 1, fmt.Errorf("%s: %w", src, err))
			}
			if err := os.RemoveAll(srcAbs); err != nil {
				return it.failCmd("mv", 1, fmt.Errorf("%s: %w", src, err))
			}
		}
	}
	it.LastStatus = 0
	return "", "", nil
}
