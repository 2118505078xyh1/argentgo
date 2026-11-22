package bash

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// safeArchiveEntryPath 校验归档条目名并锚定到工作区：
// 拒绝绝对路径、盘符/UNC 以及任何 ".." 组件，避免解包时穿越出工作区。
// 校验通过后统一走 resolvePath，由它再做一次词法与真实路径边界检查。
func (it *Interp) safeArchiveEntryPath(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("archive entry has empty name")
	}
	n := strings.ReplaceAll(name, "\\", "/") // 统一分隔符，堵住 Windows 上的 \.. 逃逸
	if strings.HasPrefix(n, "/") || isAbs(name) || filepath.IsAbs(name) {
		return "", fmt.Errorf("archive entry %q is absolute", name)
	}
	if strings.HasPrefix(n, "//") {
		return "", fmt.Errorf("archive entry %q is UNC", name)
	}
	for _, part := range strings.Split(n, "/") {
		if part == ".." {
			return "", fmt.Errorf("archive entry %q contains '..'", name)
		}
		if len(part) >= 2 && part[1] == ':' {
			return "", fmt.Errorf("archive entry %q contains a drive letter", name)
		}
	}
	return it.resolvePath(filepath.FromSlash(n))
}

// archiveEntryName 把工作区内的绝对路径转成归档条目名（相对 Cwd，正斜杠）。
func (it *Interp) archiveEntryName(abs string) (string, error) {
	base := it.Cwd
	if base == "" {
		base = "."
	}
	baseAbs, err := filepath.Abs(base)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(baseAbs, abs)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}

// biTar 实现 tar 子集：-c 创建、-x 解包、-t 列出、-z gzip、-f 指定文件。
// 无 -f 时二进制数据经 stdin/返回值传递（Go string 可承载任意字节）。
func (it *Interp) biTar(args []string, stdin string) (string, string, error) {
	p, err := parseShortFlags(args, "cxtzf:v", "f")
	if err != nil {
		return it.failCmd("tar", 2, err)
	}
	create, extract, list := hasFlag(p, 'c'), hasFlag(p, 'x'), hasFlag(p, 't')
	useGzip, verbose := hasFlag(p, 'z'), hasFlag(p, 'v')
	file := p.flags['f']
	modes := 0
	for _, m := range []bool{create, extract, list} {
		if m {
			modes++
		}
	}
	if modes != 1 {
		return it.failCmd("tar", 2, fmt.Errorf("exactly one of -c, -x, -t is required"))
	}
	if create && len(p.operands) == 0 {
		return it.failCmd("tar", 2, fmt.Errorf("cowardly refusing to create an empty archive"))
	}
	switch {
	case create:
		return it.tarCreate(p.operands, file, useGzip)
	case list:
		return it.tarList(file, stdin, useGzip, verbose)
	default:
		return it.tarExtract(file, stdin, useGzip)
	}
}

// tarCreate 递归打包_paths；每个路径与遍历到的文件都经 resolvePath 校验。
func (it *Interp) tarCreate(paths []string, file string, useGzip bool) (string, string, error) {
	var buf bytes.Buffer
	var raw io.Writer = &buf
	var closers []io.Closer
	if file != "" {
		abs, err := it.resolvePath(file)
		if err != nil {
			return it.failCmd("tar", 1, err)
		}
		f, err := os.Create(abs)
		if err != nil {
			return it.failCmd("tar", 1, fmt.Errorf("%s: %w", file, err))
		}
		raw, closers = f, append(closers, f)
	}
	var gw *gzip.Writer
	if useGzip {
		gw = gzip.NewWriter(raw)
		raw = gw
	}
	tw := tar.NewWriter(raw)
	for _, op := range paths {
		abs, err := it.resolvePath(op)
		if err != nil {
			return it.failCmd("tar", 1, err)
		}
		if err := it.tarWalk(tw, abs); err != nil {
			return it.failCmd("tar", 1, err)
		}
	}
	if err := tw.Close(); err != nil {
		return it.failCmd("tar", 1, err)
	}
	if gw != nil {
		if err := gw.Close(); err != nil {
			return it.failCmd("tar", 1, err)
		}
	}
	for _, c := range closers {
		_ = c.Close()
	}
	if file != "" {
		return "", "", nil
	}
	return buf.String(), "", nil
}

// tarWalk 用 WalkDir 递归写入一个路径；符号链接记录链接目标但不跟随。
func (it *Interp) tarWalk(tw *tar.Writer, root string) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if _, e := it.resolvePath(path); e != nil {
			return e
		}
		name, e := it.archiveEntryName(path)
		if e != nil {
			return e
		}
		info, e := d.Info()
		if e != nil {
			return e
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			link, e = os.Readlink(path)
			if e != nil {
				return e
			}
		}
		hdr, e := tar.FileInfoHeader(info, link)
		if e != nil {
			return e
		}
		hdr.Name = name
		if info.IsDir() && !strings.HasSuffix(hdr.Name, "/") {
			hdr.Name += "/"
		}
		if e := tw.WriteHeader(hdr); e != nil {
			return e
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		f, e := os.Open(path)
		if e != nil {
			return e
		}
		_, e = io.Copy(tw, f)
		_ = f.Close()
		return e
	})
}

// tarReader 按 -f/stdin 与 -z 组装读取链，返回 reader 与关闭函数。
func (it *Interp) tarReader(file, stdin string, useGzip bool) (io.Reader, func(), error) {
	var in io.Reader
	var closers []io.Closer
	if file != "" {
		abs, err := it.resolvePath(file)
		if err != nil {
			return nil, nil, err
		}
		f, err := os.Open(abs)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", file, err)
		}
		in, closers = f, append(closers, f)
	} else {
		in = bytes.NewReader([]byte(stdin))
	}
	if useGzip {
		gr, err := gzip.NewReader(in)
		if err != nil {
			return nil, nil, err
		}
		in, closers = gr, append(closers, gr)
	}
	closeFn := func() {
		for _, c := range closers {
			_ = c.Close()
		}
	}
	return in, closeFn, nil
}

// tarList 对齐 GNU tar -t（名称）与 -tv（权限/大小/日期/名称）格式。
func (it *Interp) tarList(file, stdin string, useGzip, verbose bool) (string, string, error) {
	in, closeFn, err := it.tarReader(file, stdin, useGzip)
	if err != nil {
		return it.failCmd("tar", 1, err)
	}
	if closeFn != nil {
		defer closeFn()
	}
	tr := tar.NewReader(in)
	var out strings.Builder
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return it.failCmd("tar", 1, err)
		}
		if verbose {
			fi := hdr.FileInfo()
			fmt.Fprintf(&out, "%s %10d %s %s\n", fi.Mode().String(), hdr.Size,
				hdr.ModTime.Format("2006-01-02 15:04"), hdr.Name)
		} else {
			out.WriteString(hdr.Name + "\n")
		}
	}
	it.LastStatus = 0
	return out.String(), "", nil
}

// tarExtract 解包到工作区，每个条目都经 safeArchiveEntryPath 校验防穿越。
func (it *Interp) tarExtract(file, stdin string, useGzip bool) (string, string, error) {
	in, closeFn, err := it.tarReader(file, stdin, useGzip)
	if err != nil {
		return it.failCmd("tar", 1, err)
	}
	if closeFn != nil {
		defer closeFn()
	}
	tr := tar.NewReader(in)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return it.failCmd("tar", 1, err)
		}
		if err := it.tarWriteEntry(tr, hdr); err != nil {
			return it.failCmd("tar", 1, err)
		}
	}
	it.LastStatus = 0
	return "", "", nil
}

// tarWriteEntry 写入单个 tar 条目；不支持的类型静默跳过。
func (it *Interp) tarWriteEntry(tr *tar.Reader, hdr *tar.Header) error {
	target, err := it.safeArchiveEntryPath(hdr.Name)
	if err != nil {
		return err
	}
	mode := hdr.FileInfo().Mode().Perm()
	switch hdr.Typeflag {
	case tar.TypeDir:
		return os.MkdirAll(target, mode|0o700)
	case tar.TypeReg, tar.TypeRegA, tar.TypeGNUSparse:
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			return err
		}
		_, err = io.Copy(f, tr)
		if e := f.Close(); err == nil {
			err = e
		}
		return err
	case tar.TypeSymlink:
		// 链接目标必须是相对且留在工作区内的路径，跳过可疑项以防后续写入穿越。
		if _, err := it.safeArchiveEntryPath(hdr.Linkname); err != nil {
			return nil
		}
		_ = os.Remove(target)
		return os.Symlink(hdr.Linkname, target)
	default:
		return nil
	}
}

// biZip 实现 zip 打包：-r 递归目录；无 -r 时只写入文件与目录条目本身。
func (it *Interp) biZip(args []string) (string, string, error) {
	p, err := parseShortFlags(args, "r", "")
	if err != nil {
		return it.failCmd("zip", 2, err)
	}
	if len(p.operands) < 2 {
		return it.failCmd("zip", 1, fmt.Errorf("missing archive or file operands"))
	}
	archive, paths := p.operands[0], p.operands[1:]
	abs, err := it.resolvePath(archive)
	if err != nil {
		return it.failCmd("zip", 1, err)
	}
	out, err := os.Create(abs)
	if err != nil {
		return it.failCmd("zip", 1, fmt.Errorf("%s: %w", archive, err))
	}
	defer out.Close()
	zw := zip.NewWriter(out)
	for _, op := range paths {
		if err := it.zipAddPath(zw, op, hasFlag(p, 'r')); err != nil {
			_ = zw.Close()
			return it.failCmd("zip", 1, err)
		}
	}
	if err := zw.Close(); err != nil {
		return it.failCmd("zip", 1, err)
	}
	it.LastStatus = 0
	return "", "", nil
}

// zipAddPath 写入一个操作数；目录条目总是写入，-r 时再递归其子项。
func (it *Interp) zipAddPath(zw *zip.Writer, op string, recursive bool) error {
	abs, err := it.resolvePath(op)
	if err != nil {
		return err
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	if !info.IsDir() {
		if info.Mode()&os.ModeSymlink != 0 {
			return nil // zip 不记录符号链接，避免写入链接目标文本
		}
		name, e := it.archiveEntryName(abs)
		if e != nil {
			return e
		}
		return it.zipAddFile(zw, abs, name, info)
	}
	name, err := it.archiveEntryName(abs)
	if err != nil {
		return err
	}
	if err := it.zipAddDir(zw, name+"/"); err != nil {
		return err
	}
	if !recursive {
		return nil
	}
	return filepath.WalkDir(abs, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == abs {
			return nil
		}
		if _, e := it.resolvePath(path); e != nil {
			return e
		}
		fi, e := d.Info()
		if e != nil {
			return e
		}
		entry, e := it.archiveEntryName(path)
		if e != nil {
			return e
		}
		if fi.IsDir() {
			return it.zipAddDir(zw, entry+"/")
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		return it.zipAddFile(zw, path, entry, fi)
	})
}

func (it *Interp) zipAddDir(zw *zip.Writer, name string) error {
	hdr := &zip.FileHeader{Name: name, Method: zip.Deflate}
	hdr.SetMode(os.ModeDir | 0o755)
	_, err := zw.CreateHeader(hdr)
	return err
}

func (it *Interp) zipAddFile(zw *zip.Writer, abs, name string, info os.FileInfo) error {
	hdr, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	hdr.Name = name
	hdr.Method = zip.Deflate
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		return err
	}
	f, err := os.Open(abs)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, f)
	_ = f.Close()
	return err
}

// biUnzip 实现 unzip 子集：-l 列出条目（大小/日期/名称），否则解压到工作区。
func (it *Interp) biUnzip(args []string) (string, string, error) {
	p, err := parseShortFlags(args, "l", "")
	if err != nil {
		return it.failCmd("unzip", 2, err)
	}
	if len(p.operands) < 1 {
		return it.failCmd("unzip", 1, fmt.Errorf("missing archive operand"))
	}
	archive, filters := p.operands[0], p.operands[1:]
	abs, err := it.resolvePath(archive)
	if err != nil {
		return it.failCmd("unzip", 1, err)
	}
	zr, err := zip.OpenReader(abs)
	if err != nil {
		return it.failCmd("unzip", 1, fmt.Errorf("%s: %w", archive, err))
	}
	defer zr.Close()
	if hasFlag(p, 'l') {
		it.LastStatus = 0
		return unzipList(zr.File), "", nil
	}
	for _, f := range zr.File {
		if !unzipWanted(f.Name, filters) {
			continue
		}
		if err := it.unzipEntry(f); err != nil {
			return it.failCmd("unzip", 1, err)
		}
	}
	it.LastStatus = 0
	return "", "", nil
}

func unzipList(files []*zip.File) string {
	var out strings.Builder
	for _, f := range files {
		fmt.Fprintf(&out, "%10d  %s  %s\n", f.UncompressedSize64,
			f.Modified.Format("2006-01-02 15:04"), f.Name)
	}
	return out.String()
}

// unzipWanted 判断条目是否匹配文件名过滤条件（精确名或基名）。
func unzipWanted(name string, filters []string) bool {
	if len(filters) == 0 {
		return true
	}
	for _, flt := range filters {
		if flt == name || filepath.Base(flt) == filepath.Base(name) {
			return true
		}
	}
	return false
}

// unzipEntry 写入单个 zip 条目；符号链接跳过，目录与文件均过边界校验。
func (it *Interp) unzipEntry(f *zip.File) error {
	target, err := it.safeArchiveEntryPath(f.Name)
	if err != nil {
		return err
	}
	if f.FileInfo().IsDir() || f.Mode()&os.ModeSymlink != 0 {
		return os.MkdirAll(target, 0o755)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode().Perm())
	if err != nil {
		return err
	}
	_, err = io.Copy(out, rc)
	if e := out.Close(); err == nil {
		err = e
	}
	return err
}

func gzipBytes(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(data); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func gunzipBytes(data []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return io.ReadAll(zr)
}

// biGzip 实现 gzip 子集：-c 输出到 stdout、-d 解压、-k 保留原文件。
// 工作区内默认保留原文件（不做删除），-k 仅为兼容 GNU 而接受。
func (it *Interp) biGzip(args []string, stdin string) (string, string, error) {
	return it.gzipRun("gzip", args, stdin)
}

// biGunzip 等价 gzip -d。
func (it *Interp) biGunzip(args []string, stdin string) (string, string, error) {
	return it.gzipRun("gunzip", append([]string{"-d"}, args...), stdin)
}

// gzipRun 是 gzip/gunzip 共用实现：无文件参数时处理 stdin 并写 stdout，
// 有文件参数时按 -c 决定输出到 stdout 还是写同目录的 *.gz / 去后缀文件。
func (it *Interp) gzipRun(cmd string, args []string, stdin string) (string, string, error) {
	p, err := parseShortFlags(args, "cdk", "")
	if err != nil {
		return it.failCmd(cmd, 2, err)
	}
	decompress, toStdout := hasFlag(p, 'd'), hasFlag(p, 'c')
	_ = hasFlag(p, 'k')
	transform := gzipBytes
	if decompress {
		transform = gunzipBytes
	}
	if len(p.operands) == 0 {
		out, err := transform([]byte(stdin))
		if err != nil {
			return it.failCmd(cmd, 1, err)
		}
		it.LastStatus = 0
		return string(out), "", nil
	}
	var result bytes.Buffer
	for _, op := range p.operands {
		abs, err := it.resolvePath(op)
		if err != nil {
			return it.failCmd(cmd, 1, err)
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			return it.failCmd(cmd, 1, fmt.Errorf("%s: %w", op, err))
		}
		out, err := transform(data)
		if err != nil {
			return it.failCmd(cmd, 1, fmt.Errorf("%s: %w", op, err))
		}
		if toStdout {
			result.Write(out)
			continue
		}
		name := op + ".gz"
		if decompress {
			if !strings.HasSuffix(op, ".gz") {
				return it.failCmd(cmd, 1, fmt.Errorf("%s: no .gz suffix", op))
			}
			name = strings.TrimSuffix(op, ".gz")
		}
		dstAbs, err := it.resolvePath(name)
		if err != nil {
			return it.failCmd(cmd, 1, err)
		}
		if err := os.WriteFile(dstAbs, out, 0o644); err != nil {
			return it.failCmd(cmd, 1, fmt.Errorf("%s: %w", name, err))
		}
	}
	it.LastStatus = 0
	return result.String(), "", nil
}
