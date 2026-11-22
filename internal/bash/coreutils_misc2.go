package bash

import (
	"bytes"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// statTimeFormat 同时用于 stat 默认输出与 %y，带纳秒和时区偏移以贴近 GNU。
	statTimeFormat = "2006-01-02 15:04:05.000000000 -0700"
	// misc2SeqMax 限制 seq 产生的数值个数，避免误写巨大区间导致输出失控。
	misc2SeqMax = 100000
	// misc2YesMax 限制 yes 的输出行数；工具场景不能无限刷屏。
	misc2YesMax = 1000
)

// biStat 输出文件信息：默认多行（File/Size/Modify/Mode），-c 支持
// %s(大小) %n(名) %a(权限八进制) %F(类型) %y(修改时间) 等格式符。
// 默认跟随 GNU stat 的语义，用 Lstat 报告链接自身而非目标。
func (it *Interp) biStat(args []string, stdin string) (string, string, error) {
	_ = stdin // 本命令不消费标准输入，保留参数以对齐内建签名。
	p, err := parseShortFlags(args, "c", "c")
	if err != nil {
		return it.failCmd("stat", 2, err)
	}
	if len(p.operands) == 0 {
		return it.failCmd("stat", 1, fmt.Errorf("missing operand"))
	}
	format, formatted := p.flags['c'], hasFlag(p, 'c')
	var out []string
	for _, op := range p.operands {
		abs, err := it.resolvePath(op)
		if err != nil {
			return it.failCmd("stat", 1, err)
		}
		info, err := os.Lstat(abs)
		if err != nil {
			return it.failCmd("stat", 1, fmt.Errorf("%s: %w", op, err))
		}
		if formatted {
			out = append(out, misc2StatFormat(format, op, info))
		} else {
			out = append(out, misc2StatDefault(op, info))
		}
	}
	it.LastStatus = 0
	return strings.Join(out, "\n") + "\n", "", nil
}

// misc2StatDefault 拼出默认的多行文件信息，字段宽度与 GNU stat 的前几行一致。
func misc2StatDefault(name string, info os.FileInfo) string {
	return fmt.Sprintf("  File: %s\n  Size: %d\nModify: %s\n  Mode: %s",
		name, info.Size(), info.ModTime().Format(statTimeFormat), misc2StatPermOct(info.Mode()))
}

// misc2StatFormat 展开 -c 格式串；未知格式符按字面保留，避免静默吞字符。
func misc2StatFormat(format, name string, info os.FileInfo) string {
	var b strings.Builder
	for i := 0; i < len(format); i++ {
		if format[i] != '%' {
			b.WriteByte(format[i])
			continue
		}
		i++
		if i >= len(format) {
			b.WriteByte('%')
			break
		}
		switch format[i] {
		case '%':
			b.WriteByte('%')
		case 'n':
			b.WriteString(name)
		case 's':
			b.WriteString(strconv.FormatInt(info.Size(), 10))
		case 'a':
			b.WriteString(misc2StatPermOct(info.Mode()))
		case 'A':
			b.WriteString(misc2StatModeSymbolic(info.Mode()))
		case 'F':
			b.WriteString(misc2StatType(info.Mode()))
		case 'y':
			b.WriteString(info.ModTime().Format(statTimeFormat))
		case 'Y':
			b.WriteString(strconv.FormatInt(info.ModTime().Unix(), 10))
		case 'b':
			b.WriteString(strconv.FormatInt((info.Size()+511)/512, 10))
		default:
			b.WriteByte('%')
			b.WriteByte(format[i])
		}
	}
	return b.String()
}

// misc2StatPermOct 把权限位转成八进制；setuid/setgid/sticky 一并计入高位。
func misc2StatPermOct(mode os.FileMode) string {
	perm := uint32(mode.Perm())
	if mode&os.ModeSetuid != 0 {
		perm |= 0o4000
	}
	if mode&os.ModeSetgid != 0 {
		perm |= 0o2000
	}
	if mode&os.ModeSticky != 0 {
		perm |= 0o1000
	}
	return strconv.FormatUint(uint64(perm), 8)
}

// misc2StatModeSymbolic 转换 Go 的链接前缀 "L" 为 GNU 使用的 "l"。
func misc2StatModeSymbolic(mode os.FileMode) string {
	s := mode.String()
	if strings.HasPrefix(s, "L") {
		s = "l" + s[1:]
	}
	return s
}

// misc2StatType 返回 GNU stat %F 风格的类型描述。
func misc2StatType(mode os.FileMode) string {
	m := mode & os.ModeType
	switch {
	case m&os.ModeSymlink != 0:
		return "symbolic link"
	case m&os.ModeDir != 0:
		return "directory"
	case m&os.ModeNamedPipe != 0:
		return "fifo"
	case m&os.ModeSocket != 0:
		return "socket"
	case m&os.ModeDevice != 0:
		if m&os.ModeCharDevice != 0 {
			return "character special file"
		}
		return "block special file"
	case m&os.ModeIrregular != 0:
		return "irregular file"
	default:
		return "regular file"
	}
}

// biReadlink 读取符号链接目标；-f 递归解析到最终目标。
// resolvePathLexical 做词法边界检查并返回绝对路径，但不调用会解析符号链接的
// security.CheckPathInRoots。专供 readlink 这类需要读取链接本身的命令使用。
func (it *Interp) resolvePathLexical(p string) (string, error) {
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
	root := it.Workspace
	if root == "" {
		root = it.Cwd
	}
	if root == "" {
		root = "."
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(rootAbs, abs)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside the workspace", p)
	}
	return abs, nil
}

func (it *Interp) biReadlink(args []string, stdin string) (string, string, error) {
	_ = stdin
	p, err := parseShortFlags(args, "f", "")
	if err != nil {
		return it.failCmd("readlink", 2, err)
	}
	if len(p.operands) != 1 {
		return it.failCmd("readlink", 1, fmt.Errorf("missing or extra operand"))
	}
	// readlink 不能用 resolvePath：后者会解析符号链接并检查真实路径边界，
	// 导致 "link" 被解析成目标文件本身，os.Readlink 读到的就不是链接了。
	// 这里只做词法边界检查（不解析符号链接），因为 readlink 只读链接文本，
	// 不读链接指向的内容，词法检查足以保证安全。
	abs, err := it.resolvePathLexical(p.operands[0])
	if err != nil {
		return it.failCmd("readlink", 1, err)
	}
	var target string
	if hasFlag(p, 'f') {
		target, err = filepath.EvalSymlinks(abs)
	} else {
		target, err = os.Readlink(abs)
	}
	if err != nil {
		return it.failCmd("readlink", 1, fmt.Errorf("%s: %w", p.operands[0], err))
	}
	it.LastStatus = 0
	return target + "\n", "", nil
}

// biRealpath 返回绝对路径并解析符号链接，路径一律经 resolvePath 校验边界。
func (it *Interp) biRealpath(args []string, stdin string) (string, string, error) {
	_ = stdin
	p, err := parseShortFlags(args, "", "")
	if err != nil {
		return it.failCmd("realpath", 2, err)
	}
	if len(p.operands) == 0 {
		return it.failCmd("realpath", 1, fmt.Errorf("missing operand"))
	}
	var out []string
	for _, op := range p.operands {
		abs, err := it.resolvePath(op)
		if err != nil {
			return it.failCmd("realpath", 1, err)
		}
		real, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return it.failCmd("realpath", 1, fmt.Errorf("%s: %w", op, err))
		}
		out = append(out, real)
	}
	it.LastStatus = 0
	return strings.Join(out, "\n") + "\n", "", nil
}

// biFile 读取文件前 512 字节，按魔数/可打印性判断类型，输出 "name: 描述"。
func (it *Interp) biFile(args []string, stdin string) (string, string, error) {
	p, err := parseShortFlags(args, "", "")
	if err != nil {
		return it.failCmd("file", 2, err)
	}
	if len(p.operands) == 0 {
		return it.failCmd("file", 1, fmt.Errorf("missing operand"))
	}
	var out []string
	for _, op := range p.operands {
		var data []byte
		if op == "-" {
			data = []byte(stdin)
		} else {
			abs, err := it.resolvePath(op)
			if err != nil {
				return it.failCmd("file", 1, err)
			}
			f, err := os.Open(abs)
			if err != nil {
				return it.failCmd("file", 1, fmt.Errorf("%s: %w", op, err))
			}
			buf := make([]byte, 512)
			n, _ := f.Read(buf) // 短读是正常现象，忽略 EOF。
			_ = f.Close()
			data = buf[:n]
		}
		out = append(out, op+": "+misc2FileDesc(data))
	}
	it.LastStatus = 0
	return strings.Join(out, "\n") + "\n", "", nil
}

// misc2FileDesc 按前 512 字节识别常见格式，未命中且可打印时归类为 text/plain。
func misc2FileDesc(data []byte) string {
	if len(data) == 0 {
		return "empty"
	}
	switch {
	case bytes.HasPrefix(data, []byte("\x7fELF")):
		return "ELF executable"
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return "PNG image data"
	case bytes.HasPrefix(data, []byte("\xff\xd8\xff")):
		return "JPEG image data"
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return "GIF image data"
	case bytes.HasPrefix(data, []byte("\x1f\x8b")):
		return "gzip compressed data"
	case bytes.HasPrefix(data, []byte("PK\x03\x04")), bytes.HasPrefix(data, []byte("PK\x05\x06")),
		bytes.HasPrefix(data, []byte("PK\x07\x08")):
		return "Zip archive data"
	case bytes.HasPrefix(data, []byte("%PDF-")):
		return "PDF document"
	case bytes.HasPrefix(data, []byte("BM")):
		return "BMP image data"
	case bytes.HasPrefix(data, []byte("7z\xbc\xaf\x27\x1c")):
		return "7-zip archive data"
	case bytes.HasPrefix(data, []byte("Rar!\x1a\x07")):
		return "RAR archive data"
	case len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "WebP image data"
	case len(data) >= 262 && bytes.Equal(data[257:262], []byte("ustar")):
		return "POSIX tar archive"
	case bytes.HasPrefix(data, []byte("MZ")):
		return "PE executable"
	}
	if misc2IsText(data) {
		return "text/plain"
	}
	return "data"
}

// misc2IsText 判定 UTF-8 可解码且除常见空白外无控制字符（含 NUL 视为二进制）。
func misc2IsText(data []byte) bool {
	if !utf8.Valid(data) {
		return false
	}
	for _, c := range data {
		if c == 0 || c == 0x7f {
			return false
		}
		if c < 0x20 && c != '\t' && c != '\n' && c != '\r' && c != '\f' && c != '\b' && c != '\v' {
			return false
		}
	}
	return true
}

// misc2SeqNum 同时保存浮点值与原始小数位数，便于输出时抑制浮点尾差。
type misc2SeqNum struct {
	f        float64
	i        int64
	decimals int
	integer  bool
}

var misc2SeqOne = misc2SeqNum{f: 1, i: 1, integer: true}

// biSeq 输出数字序列：seq LAST / seq FIRST LAST / seq FIRST INCREMENT LAST
// （GNU 三参数顺序），-s 指定分隔符，支持整数与简单浮点。
func (it *Interp) biSeq(args []string, stdin string) (string, string, error) {
	_ = stdin
	p, err := parseShortFlags(args, "s", "s")
	if err != nil {
		return it.failCmd("seq", 2, err)
	}
	if len(p.operands) < 1 || len(p.operands) > 3 {
		return it.failCmd("seq", 1, fmt.Errorf("invalid number of operands"))
	}
	sep := "\n"
	if hasFlag(p, 's') {
		sep = p.flags['s']
	}
	nums := make([]misc2SeqNum, len(p.operands))
	for i, op := range p.operands {
		nums[i], err = misc2ParseSeqNum(op)
		if err != nil {
			return it.failCmd("seq", 1, err)
		}
	}
	first, step, last := misc2SeqOne, misc2SeqOne, misc2SeqOne
	switch len(nums) {
	case 1:
		last = nums[0]
	case 2:
		first, last = nums[0], nums[1]
	case 3:
		first, step, last = nums[0], nums[1], nums[2]
	}
	var out string
	if first.integer && step.integer && last.integer {
		out, err = misc2SeqIntegers(first.i, step.i, last.i, sep)
	} else {
		out, err = misc2SeqFloats(first, step, last, sep)
	}
	if err != nil {
		return it.failCmd("seq", 1, err)
	}
	it.LastStatus = 0
	return out, "", nil
}

// misc2ParseSeqNum 优先按 int64 解析，失败再按 float64，并记录小数位。
func misc2ParseSeqNum(s string) (misc2SeqNum, error) {
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return misc2SeqNum{f: float64(i), i: i, integer: true}, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return misc2SeqNum{}, fmt.Errorf("invalid number %q", s)
	}
	d := 0
	if dot := strings.IndexByte(s, '.'); dot >= 0 {
		d = len(s) - dot - 1
	}
	return misc2SeqNum{f: f, decimals: d}, nil
}

// misc2SeqIntegers 用整数运算生成序列，避免大整数被浮点截断。
func misc2SeqIntegers(first, step, last int64, sep string) (string, error) {
	if step == 0 {
		return "", fmt.Errorf("zero increment")
	}
	var count int64
	if (step > 0 && first <= last) || (step < 0 && first >= last) {
		count = (last-first)/step + 1
	}
	if count <= 0 {
		return "", nil
	}
	if count > misc2SeqMax {
		count = misc2SeqMax
	}
	vals := make([]string, 0, count)
	for i := int64(0); i < count; i++ {
		vals = append(vals, strconv.FormatInt(first+i*step, 10))
	}
	return strings.Join(vals, sep) + "\n", nil
}

// misc2SeqFloats 用输入的小数位数格式化，抑制 0.1+0.2 一类的浮点尾差。
func misc2SeqFloats(first, step, last misc2SeqNum, sep string) (string, error) {
	if step.f == 0 {
		return "", fmt.Errorf("zero increment")
	}
	decimals := first.decimals
	if step.decimals > decimals {
		decimals = step.decimals
	}
	if last.decimals > decimals {
		decimals = last.decimals
	}
	if decimals > 9 {
		decimals = 9
	}
	var count int64
	if (step.f > 0 && first.f <= last.f) || (step.f < 0 && first.f >= last.f) {
		n := (last.f - first.f) / step.f
		count = int64(n) + 1
		if n > 0 && n-float64(int64(n)) > 0.999999999 {
			count++ // 修正浮点除法把整数结果压小的情形。
		}
	}
	if count <= 0 {
		return "", nil
	}
	if count > misc2SeqMax {
		count = misc2SeqMax
	}
	vals := make([]string, 0, count)
	for i := int64(0); i < count; i++ {
		vals = append(vals, strconv.FormatFloat(first.f+float64(i)*step.f, 'f', decimals, 64))
	}
	return strings.Join(vals, sep) + "\n", nil
}

// biYes 重复输出字符串；工具场景固定上限并在 stderr 提示，同时响应取消。
func (it *Interp) biYes(args []string, stdin string) (string, string, error) {
	_ = stdin
	p, err := parseShortFlags(args, "", "")
	if err != nil {
		return it.failCmd("yes", 2, err)
	}
	word := strings.Join(p.operands, " ")
	if word == "" {
		word = "y"
	}
	line := word + "\n"
	var b strings.Builder
	b.Grow(len(line) * misc2YesMax)
	for n := 0; n < misc2YesMax; n++ {
		select {
		case <-it.ctx().Done():
			it.LastStatus = 0
			return b.String(), "", nil
		default:
		}
		b.WriteString(line)
	}
	it.LastStatus = 0
	return b.String(), fmt.Sprintf("shell: yes: 已达到 %d 行输出上限，提前停止\n", misc2YesMax), nil
}

// biShuf 用 math/rand 打乱输入行；无文件参数时读 stdin，"-" 也代表 stdin。
func (it *Interp) biShuf(args []string, stdin string) (string, string, error) {
	p, err := parseShortFlags(args, "", "")
	if err != nil {
		return it.failCmd("shuf", 2, err)
	}
	var b strings.Builder
	for _, op := range p.operands {
		if op == "-" {
			b.WriteString(stdin)
			continue
		}
		abs, err := it.resolvePath(op)
		if err != nil {
			return it.failCmd("shuf", 1, err)
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			return it.failCmd("shuf", 1, fmt.Errorf("%s: %w", op, err))
		}
		b.Write(data)
	}
	if len(p.operands) == 0 {
		b.WriteString(stdin)
	}
	lines := splitLines(b.String())
	if len(lines) == 0 {
		it.LastStatus = 0
		return "", "", nil
	}
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	r.Shuffle(len(lines), func(i, j int) { lines[i], lines[j] = lines[j], lines[i] })
	it.LastStatus = 0
	return strings.Join(lines, "\n") + "\n", "", nil
}

// biMktemp 在工作区内创建临时文件/目录，模板末尾的 XXXXXX 替换为随机串。
func (it *Interp) biMktemp(args []string, stdin string) (string, string, error) {
	_ = stdin
	p, err := parseShortFlags(args, "d", "")
	if err != nil {
		return it.failCmd("mktemp", 2, err)
	}
	if len(p.operands) > 1 {
		return it.failCmd("mktemp", 1, fmt.Errorf("too many templates"))
	}
	tmpl := "tmp.XXXXXX"
	if len(p.operands) == 1 {
		tmpl = p.operands[0]
	}
	dir, base := filepath.Split(tmpl)
	if base == "" {
		return it.failCmd("mktemp", 1, fmt.Errorf("template %q has no filename", tmpl))
	}
	pattern, err := misc2MktempPattern(base)
	if err != nil {
		return it.failCmd("mktemp", 1, err)
	}
	resolveDir := dir
	if resolveDir == "" {
		resolveDir = "."
	}
	absDir, err := it.resolvePath(resolveDir)
	if err != nil {
		return it.failCmd("mktemp", 1, err)
	}
	var created string
	if hasFlag(p, 'd') {
		created, err = os.MkdirTemp(absDir, pattern)
	} else {
		var f *os.File
		f, err = os.CreateTemp(absDir, pattern)
		if err == nil {
			created = f.Name()
			err = f.Close()
		}
	}
	if err != nil {
		return it.failCmd("mktemp", 1, fmt.Errorf("%s: %w", tmpl, err))
	}
	if _, err := it.resolvePath(created); err != nil {
		return it.failCmd("mktemp", 1, err) // 二次确认结果未逃逸工作区。
	}
	// 打印时保留用户输入模板的相对/绝对形态，与 GNU mktemp 一致。
	it.LastStatus = 0
	return filepath.Join(dir, filepath.Base(created)) + "\n", "", nil
}

// misc2MktempPattern 把 GNU 风格的尾部 XXXXXX 转成 Go CreateTemp 需要的 "*"。
func misc2MktempPattern(base string) (string, error) {
	n := 0
	for i := len(base) - 1; i >= 0 && base[i] == 'X'; i-- {
		n++
	}
	if n < 3 {
		return "", fmt.Errorf("too few X's in template %q", base)
	}
	return base[:len(base)-n] + "*", nil
}
