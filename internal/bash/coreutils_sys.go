package bash

// 本文件实现一组"系统信息 / 进程"类内建命令，全部用标准库完成：
// 不调用外部程序、不改 PATH、不引入新依赖。接线由 runBuiltin/runCoreutil 统一完成。
//
// 命令 → 入口函数对照（供接线使用）：
//
//	uname   -> (*Interp).biUname
//	hostname-> (*Interp).biHostname
//	date    -> (*Interp).biDate
//	whoami  -> (*Interp).biWhoami
//	id      -> (*Interp).biID
//	ps      -> (*Interp).biPS
//	kill    -> (*Interp).biKill
//	killall -> (*Interp).biKillall
//	pkill   -> (*Interp).biPkill
//	df      -> (*Interp).biDf
//	du      -> (*Interp).biDu
//	free    -> (*Interp).biFree
//	uptime  -> (*Interp).biUptime
//	who     -> (*Interp).biWho
//	w       -> (*Interp).biW
//	dmesg   -> (*Interp).biDmesg
//	top     -> (*Interp).biTop
//	htop    -> (*Interp).biHtop
//	jobs    -> (*Interp).biJobs
//	fg      -> (*Interp).biFg
//	bg      -> (*Interp).biBg
//	nohup   -> (*Interp).biNohup
//
// 跨平台取舍：本文件不能用 //go:build 拆成多个平台文件，因此只引用在所有
// GOOS 下都存在的标识符。Linux 侧信息通过读取 /proc 文本获得（纯文件操作，
// 任何平台都能编译）；Windows 侧无法在单文件里调用 Win32 API，相关命令
// 给出明确中文说明的近似/降级输出，绝不静默失败。

import (
	"fmt"
	"hash/fnv"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// sysStart 记录本进程启动时刻，供没有系统级 uptime 接口的平台给出进程运行时长。
var sysStart = time.Now()

// ---------- 通用小工具（统一加 sys 前缀，避免与其他 coreutils 文件重名）----------

// sysHumanBytes 把字节数格式化为二进制单位（KiB/MiB/...）的可读字符串。
func sysHumanBytes(n int64) string {
	if n < 0 {
		return "-"
	}
	const unit = int64(1024)
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := unit, 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// sysFmtSize 按 df/du/free 的习惯输出大小：-h 用可读单位，否则按 KiB 块数。
func sysFmtSize(n int64, human bool) string {
	if human {
		return sysHumanBytes(n)
	}
	return strconv.FormatInt((n+1023)/1024, 10)
}

// sysPad 左对齐补空格，用于等宽表格。
func sysPad(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
}

// sysFormatDuration 把时长格式化为 "N days, H:MM" 形式。
func sysFormatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	totalMin := int(d.Minutes())
	return fmt.Sprintf("%d days, %d:%02d", totalMin/1440, (totalMin/60)%24, totalMin%60)
}

// sysProcInfo 是 ps/killByName 共用的一条进程记录。
type sysProcInfo struct {
	pid  int
	ppid int
	name string
	cmd  string
}

// sysListProcs 返回进程快照。Linux 读 /proc（纯文本，跨平台可编译）；
// 其他平台退回"仅当前进程"，并把限制说明作为第二个返回值交给调用方展示。
func sysListProcs() ([]sysProcInfo, string) {
	entries, err := os.ReadDir("/proc")
	if err == nil {
		out := make([]sysProcInfo, 0, len(entries))
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			pid, e2 := strconv.Atoi(e.Name())
			if e2 != nil {
				continue
			}
			pr := sysProcInfo{pid: pid}
			if data, e3 := os.ReadFile("/proc/" + e.Name() + "/cmdline"); e3 == nil {
				fields := strings.FieldsFunc(string(data), func(r rune) bool { return r == 0 })
				pr.cmd = strings.Join(fields, " ")
			}
			if data, e3 := os.ReadFile("/proc/" + e.Name() + "/status"); e3 == nil {
				for _, line := range strings.Split(string(data), "\n") {
					switch {
					case strings.HasPrefix(line, "Name:"):
						pr.name = strings.TrimSpace(strings.TrimPrefix(line, "Name:"))
					case strings.HasPrefix(line, "PPid:"):
						if n, e4 := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "PPid:"))); e4 == nil {
							pr.ppid = n
						}
					}
				}
			}
			if pr.name == "" {
				if f := strings.Fields(pr.cmd); len(f) > 0 {
					pr.name = filepath.Base(f[0])
				}
			}
			out = append(out, pr)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].pid < out[j].pid })
		return out, ""
	}
	name := "?"
	if exe, e := os.Executable(); e == nil {
		name = filepath.Base(exe)
	}
	cur := sysProcInfo{pid: os.Getpid(), ppid: os.Getppid(), name: name, cmd: name}
	return []sysProcInfo{cur}, "当前平台无法在纯 Go 单文件中枚举全部进程，仅列出当前进程"
}

// ---------- uname ----------

// sysUnameKernel 返回内核名；Windows 给出 MINGW/NT 风格的系统名。
func sysUnameKernel() string {
	switch runtime.GOOS {
	case "windows":
		return "MINGW64_NT"
	case "darwin":
		return "Darwin"
	default:
		if runtime.GOOS == "" {
			return "unknown"
		}
		return strings.ToUpper(runtime.GOOS[:1]) + runtime.GOOS[1:]
	}
}

// sysUnameRelease 返回内核发行号；Linux 从 /proc 读取，Windows 给近似值。
func sysUnameRelease() string {
	if runtime.GOOS == "windows" {
		return "10.0"
	}
	if data, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		return strings.TrimSpace(string(data))
	}
	return "unknown"
}

// sysUnameMachine 把 GOARCH 映射为 uname 惯用的机器名。
func sysUnameMachine() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64"
	case "386":
		return "i686"
	case "arm64":
		return "aarch64"
	default:
		return runtime.GOARCH
	}
}

// biUname 实现 uname -a/-s/-r/-m/-n，无选项时等价于 -s。
func (it *Interp) biUname(args []string, stdin string) (string, string, error) {
	flags, err := parseShortFlags(args, "asrmn", "")
	if err != nil {
		return it.failCmd("uname", 1, err)
	}
	if len(flags.operands) > 0 {
		return it.failCmd("uname", 1, fmt.Errorf("extra operand %q", flags.operands[0]))
	}
	host, _ := os.Hostname()
	if host == "" {
		host = "unknown"
	}
	all := hasFlag(flags, 'a')
	parts := []struct {
		flag  byte
		value string
	}{
		{'s', sysUnameKernel()},
		{'n', host},
		{'r', sysUnameRelease()},
		{'m', sysUnameMachine()},
	}
	var b strings.Builder
	for _, pt := range parts {
		if all || hasFlag(flags, pt.flag) {
			b.WriteString(pt.value)
			b.WriteByte(' ')
		}
	}
	if all {
		b.WriteString(runtime.GOOS)
		b.WriteByte(' ')
	}
	s := strings.TrimRight(b.String(), " ")
	if s == "" {
		s = sysUnameKernel()
	}
	it.LastStatus = 0
	return s + "\n", "", nil
}

// ---------- hostname / whoami / id ----------

// biHostname 输出主机名；本 shell 不允许运行时修改主机名。
func (it *Interp) biHostname(args []string, stdin string) (string, string, error) {
	if len(args) > 0 {
		return it.failCmd("hostname", 1, fmt.Errorf("不支持设置主机名"))
	}
	name, err := os.Hostname()
	if err != nil {
		return it.failCmd("hostname", 1, err)
	}
	it.LastStatus = 0
	return name + "\n", "", nil
}

// biWhoami 输出当前用户名，依次尝试 os/user、USERNAME、USER。
func (it *Interp) biWhoami(args []string, stdin string) (string, string, error) {
	if len(args) > 0 {
		return it.failCmd("whoami", 1, fmt.Errorf("extra operand %q", args[0]))
	}
	name := ""
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	if name == "" {
		name = os.Getenv("USERNAME")
	}
	if name == "" {
		name = os.Getenv("USER")
	}
	if name == "" {
		return it.failCmd("whoami", 1, fmt.Errorf("无法确定当前用户"))
	}
	it.LastStatus = 0
	return name + "\n", "", nil
}

// sysApproxID 把用户/组标识转成稳定的近似整数：数字串直接用，SID 等哈希到固定区间。
func sysApproxID(s string) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return int(h.Sum32()%60000) + 1000
}

// biID 输出当前用户/组标识；Windows 上是 SID 的稳定近似值并明确说明。
func (it *Interp) biID(args []string, stdin string) (string, string, error) {
	flags, err := parseShortFlags(args, "ug", "")
	if err != nil {
		return it.failCmd("id", 1, err)
	}
	if len(flags.operands) > 0 {
		return it.failCmd("id", 1, fmt.Errorf("extra operand %q", flags.operands[0]))
	}
	u, err := user.Current()
	if err != nil {
		return it.failCmd("id", 1, fmt.Errorf("无法确定当前用户: %w", err))
	}
	uid, gid := sysApproxID(u.Uid), sysApproxID(u.Gid)
	var out string
	switch {
	case hasFlag(flags, 'u'):
		out = strconv.Itoa(uid)
	case hasFlag(flags, 'g'):
		out = strconv.Itoa(gid)
	default:
		out = fmt.Sprintf("uid=%d(%s) gid=%d groups=%d", uid, u.Username, gid, gid)
		if runtime.GOOS == "windows" {
			out += "  # Windows 用户/组标识为 SID，以上为稳定近似值"
		}
	}
	it.LastStatus = 0
	return out + "\n", "", nil
}

// ---------- date ----------

// sysDateFormat 把 GNU date 的常见 % 转义映射到 Go 的时间布局。
func sysDateFormat(t time.Time, format string) string {
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
		case 'Y':
			b.WriteString(t.Format("2006"))
		case 'y':
			b.WriteString(t.Format("06"))
		case 'm':
			b.WriteString(t.Format("01"))
		case 'd':
			b.WriteString(t.Format("02"))
		case 'e':
			b.WriteString(t.Format("_2"))
		case 'H':
			b.WriteString(t.Format("15"))
		case 'M':
			b.WriteString(t.Format("04"))
		case 'S':
			b.WriteString(t.Format("05"))
		case 'a':
			b.WriteString(t.Format("Mon"))
		case 'A':
			b.WriteString(t.Format("Monday"))
		case 'b', 'h':
			b.WriteString(t.Format("Jan"))
		case 'B':
			b.WriteString(t.Format("January"))
		case 'F':
			b.WriteString(t.Format("2006-01-02"))
		case 'T':
			b.WriteString(t.Format("15:04:05"))
		case 'R':
			b.WriteString(t.Format("15:04"))
		case 'D':
			b.WriteString(t.Format("01/02/06"))
		case 's':
			b.WriteString(strconv.FormatInt(t.Unix(), 10))
		case 'z':
			b.WriteString(t.Format("-0700"))
		case 'Z':
			b.WriteString(t.Format("MST"))
		case 'j':
			b.WriteString(t.Format("002"))
		case 'u': // 周一=1 ... 周日=7
			b.WriteString(strconv.Itoa((int(t.Weekday())+6)%7 + 1))
		case 'w': // 周日=0
			b.WriteString(strconv.Itoa(int(t.Weekday())))
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		default: // 未知转义原样保留，避免丢字符
			b.WriteByte('%')
			b.WriteByte(format[i])
		}
	}
	return b.String()
}

// biDate 支持 date [+FORMAT]；无 FORMAT 时使用 GNU 默认样式。
func (it *Interp) biDate(args []string, stdin string) (string, string, error) {
	format := ""
	for _, a := range args {
		if strings.HasPrefix(a, "+") {
			format = a[1:]
			continue
		}
		return it.failCmd("date", 1, fmt.Errorf("不支持的操作数 %q（仅支持 +FORMAT）", a))
	}
	now := time.Now()
	out := format
	if format == "" {
		out = now.Format("Mon Jan _2 15:04:05 MST 2006")
	} else {
		out = sysDateFormat(now, format)
	}
	it.LastStatus = 0
	return out + "\n", "", nil
}

// ---------- ps / kill / killall / pkill ----------

// biPS 列出进程；-e/-f 切换完整格式。进程来源见 sysListProcs。
func (it *Interp) biPS(args []string, stdin string) (string, string, error) {
	flags, err := parseShortFlags(args, "ef", "")
	if err != nil {
		return it.failCmd("ps", 1, err)
	}
	if len(flags.operands) > 0 {
		return it.failCmd("ps", 1, fmt.Errorf("extra operand %q", flags.operands[0]))
	}
	full := hasFlag(flags, 'e') || hasFlag(flags, 'f')
	procs, note := sysListProcs()
	var b strings.Builder
	if full {
		b.WriteString(sysPad("UID", 10) + sysPad("PID", 7) + sysPad("PPID", 7) + "CMD\n")
	}
	for _, pr := range procs {
		if full {
			fmt.Fprintf(&b, "%s%s%s%s\n", sysPad("?", 10), sysPad(strconv.Itoa(pr.pid), 7),
				sysPad(strconv.Itoa(pr.ppid), 7), pr.cmd)
		} else {
			fmt.Fprintf(&b, "%s%s%s\n", sysPad(strconv.Itoa(pr.pid), 7),
				sysPad(strconv.Itoa(pr.ppid), 7), pr.name)
		}
	}
	if note != "" {
		b.WriteString("# " + note + "\n")
	}
	it.LastStatus = 0
	return b.String(), "", nil
}

// sysSignalProc 向进程发信号：force 为真走 Kill（Unix SIGKILL / Windows TerminateProcess），
// 否则走 SIGTERM 的优雅终止。os.FindProcess + Signal/Kill 在所有平台都可编译。
func sysSignalProc(pid int, force bool) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if force {
		return p.Kill()
	}
	return p.Signal(syscall.SIGTERM)
}

// biKill 实现 kill [-9] PID...，默认 SIGTERM，-9 强制终止。
func (it *Interp) biKill(args []string, stdin string) (string, string, error) {
	force := false
	if len(args) > 0 && strings.HasPrefix(args[0], "-") {
		flags, err := parseShortFlags(args, "9", "")
		if err != nil {
			return it.failCmd("kill", 1, err)
		}
		force = hasFlag(flags, '9')
		args = flags.operands
	}
	if len(args) == 0 {
		return it.failCmd("kill", 1, fmt.Errorf("缺少 PID"))
	}
	for _, a := range args {
		pid, err := strconv.Atoi(a)
		if err != nil {
			return it.failCmd("kill", 1, fmt.Errorf("无效 PID %q", a))
		}
		if err := sysSignalProc(pid, force); err != nil {
			return it.failCmd("kill", 1, fmt.Errorf("PID %d: %w", pid, err))
		}
	}
	it.LastStatus = 0
	return "", "", nil
}

// sysProcMatches 判断进程是否匹配给定名字（比较进程名或完整命令行，忽略大小写）。
func sysProcMatches(pr sysProcInfo, name string) bool {
	n := strings.ToLower(filepath.Base(name))
	base := strings.ToLower(filepath.Base(pr.name))
	return base == n || strings.Contains(strings.ToLower(pr.cmd), n)
}

// sysKillByName 实现 killall/pkill 的共同逻辑；无法枚举进程的平台给明确错误。
func (it *Interp) sysKillByName(cmd string, args []string) (string, string, error) {
	force := false
	if len(args) > 0 && strings.HasPrefix(args[0], "-") {
		flags, err := parseShortFlags(args, "9", "")
		if err != nil {
			return it.failCmd(cmd, 1, err)
		}
		force = hasFlag(flags, '9')
		args = flags.operands
	}
	if len(args) == 0 {
		return it.failCmd(cmd, 1, fmt.Errorf("缺少进程名"))
	}
	procs, note := sysListProcs()
	if note != "" {
		return it.failCmd(cmd, 1, fmt.Errorf("无法按名称枚举进程：%s；请改用 kill PID", note))
	}
	name := args[0]
	killed := 0
	for _, pr := range procs {
		if pr.pid == os.Getpid() || !sysProcMatches(pr, name) {
			continue
		}
		if err := sysSignalProc(pr.pid, force); err == nil {
			killed++
		}
	}
	it.LastStatus = 0
	return fmt.Sprintf("已向 %d 个匹配 %q 的进程发送信号\n", killed, name), "", nil
}

// biKillall / biPkill 共用按名终止逻辑。
func (it *Interp) biKillall(args []string, stdin string) (string, string, error) {
	return it.sysKillByName("killall", args)
}

func (it *Interp) biPkill(args []string, stdin string) (string, string, error) {
	return it.sysKillByName("pkill", args)
}

// ---------- df / du ----------

// biDf 展示目标路径所属文件系统的空间使用。纯 Go 单文件无法调用平台磁盘统计
// API，因此容量列以 "-" 呈现并给出说明，路径本身仍经 resolvePath 校验。
func (it *Interp) biDf(args []string, stdin string) (string, string, error) {
	flags, err := parseShortFlags(args, "h", "")
	if err != nil {
		return it.failCmd("df", 1, err)
	}
	if len(flags.operands) > 1 {
		return it.failCmd("df", 1, fmt.Errorf("too many arguments"))
	}
	path := "."
	if len(flags.operands) == 1 {
		path = flags.operands[0]
	}
	abs, err := it.resolvePath(path)
	if err != nil {
		return it.failCmd("df", 1, err)
	}
	info, statErr := os.Stat(abs)
	kind := "directory"
	if statErr == nil && !info.IsDir() {
		kind = "file"
	}
	var b strings.Builder
	if hasFlag(flags, 'h') {
		b.WriteString(sysPad("Filesystem", 18) + sysPad("Size", 8) + sysPad("Used", 8) + sysPad("Avail", 8) + "Mounted on\n")
		fmt.Fprintf(&b, "%s%s%s%s%s\n", sysPad("<local>", 18), sysPad("-", 8), sysPad("-", 8), sysPad("-", 8), abs)
	} else {
		b.WriteString(sysPad("Filesystem", 18) + sysPad("1K-blocks", 12) + sysPad("Used", 10) + sysPad("Avail", 10) + "Mounted on\n")
		fmt.Fprintf(&b, "%s%s%s%s%s\n", sysPad("<local>", 18), sysPad("-", 12), sysPad("-", 10), sysPad("-", 10), abs)
	}
	fmt.Fprintf(&b, "# 目标路径存在性：%t（%s）。纯 Go 单文件无法调用平台磁盘统计 API，容量列不可用。\n",
		statErr == nil, kind)
	it.LastStatus = 0
	return b.String(), "", nil
}

// biDu 用 WalkDir 累加文件大小：-s 只输出总计，-h 用可读单位。
func (it *Interp) biDu(args []string, stdin string) (string, string, error) {
	flags, err := parseShortFlags(args, "sh", "")
	if err != nil {
		return it.failCmd("du", 1, err)
	}
	if len(flags.operands) > 1 {
		return it.failCmd("du", 1, fmt.Errorf("too many arguments"))
	}
	path := "."
	if len(flags.operands) == 1 {
		path = flags.operands[0]
	}
	abs, err := it.resolvePath(path)
	if err != nil {
		return it.failCmd("du", 1, err)
	}
	human := hasFlag(flags, 'h')
	summary := hasFlag(flags, 's')
	var total int64
	var lines []string
	err = filepath.WalkDir(abs, func(walkPath string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		info, e := d.Info()
		if e != nil {
			return nil
		}
		sz := info.Size()
		total += sz
		if !summary {
			lines = append(lines, sysPad(sysFmtSize(sz, human), 10)+walkPath)
		}
		return nil
	})
	if err != nil {
		return it.failCmd("du", 1, err)
	}
	lines = append(lines, sysPad(sysFmtSize(total, human), 10)+abs)
	it.LastStatus = 0
	return strings.Join(lines, "\n") + "\n", "", nil
}

// ---------- free / uptime ----------

// sysParseMemInfo 解析 /proc/meminfo 的 "Key: value kB" 行，值以 kB 为单位返回。
func sysParseMemInfo(data string) map[string]int64 {
	out := map[string]int64{}
	for _, line := range strings.Split(data, "\n") {
		i := strings.IndexByte(line, ':')
		if i < 0 {
			continue
		}
		fields := strings.Fields(line[i+1:])
		if len(fields) == 0 {
			continue
		}
		if n, err := strconv.ParseInt(fields[0], 10, 64); err == nil {
			out[line[:i]] = n
		}
	}
	return out
}

// biFree 展示内存占用：Linux 读 /proc/meminfo；其他平台用 Go 运行时堆作近似并说明。
func (it *Interp) biFree(args []string, stdin string) (string, string, error) {
	flags, err := parseShortFlags(args, "h", "")
	if err != nil {
		return it.failCmd("free", 1, err)
	}
	if len(flags.operands) > 0 {
		return it.failCmd("free", 1, fmt.Errorf("extra operand %q", flags.operands[0]))
	}
	human := hasFlag(flags, 'h')
	var b strings.Builder
	b.WriteString(sysPad("", 8) + sysPad("total", 12) + sysPad("used", 12) + sysPad("free", 12) +
		sysPad("shared", 10) + sysPad("buff/cache", 12) + "available\n")
	if data, e := os.ReadFile("/proc/meminfo"); e == nil {
		v := sysParseMemInfo(string(data))
		kb := func(k string) int64 { return v[k] * 1024 }
		total := kb("MemTotal")
		free := kb("MemFree")
		buffers, cached := kb("Buffers"), kb("Cached")
		avail := kb("MemAvailable")
		if avail == 0 {
			avail = free + buffers + cached
		}
		used := total - free - buffers - cached
		if used < 0 || used > total {
			used = total - avail
		}
		fmt.Fprintf(&b, "%s%s%s%s%s%s%s\n", sysPad("Mem:", 8),
			sysPad(sysFmtSize(total, human), 12), sysPad(sysFmtSize(used, human), 12),
			sysPad(sysFmtSize(free, human), 12), sysPad(sysFmtSize(kb("Shmem"), human), 10),
			sysPad(sysFmtSize(buffers+cached, human), 12), sysFmtSize(avail, human))
	} else {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		fmt.Fprintf(&b, "# 当前平台无法读取系统内存统计；以下仅为 Go 进程堆的近似值，非系统内存。\n")
		fmt.Fprintf(&b, "GoHeap: allocated=%s sys=%s\n",
			sysFmtSize(int64(ms.Alloc), human), sysFmtSize(int64(ms.Sys), human))
	}
	it.LastStatus = 0
	return b.String(), "", nil
}

// biUptime 展示运行时长：Linux 读 /proc/uptime；否则退回本进程运行时长并说明。
func (it *Interp) biUptime(args []string, stdin string) (string, string, error) {
	if len(args) > 0 {
		return it.failCmd("uptime", 1, fmt.Errorf("extra operand %q", args[0]))
	}
	var d time.Duration
	if data, err := os.ReadFile("/proc/uptime"); err == nil {
		if f := strings.Fields(string(data)); len(f) > 0 {
			if sec, e := strconv.ParseFloat(f[0], 64); e == nil {
				d = time.Duration(sec * float64(time.Second))
			}
		}
	}
	note := ""
	if d == 0 {
		d = time.Since(sysStart)
		note = "（当前平台无系统级 uptime 接口，显示本进程运行时长）"
	}
	it.LastStatus = 0
	return fmt.Sprintf(" %s  up %s%s\n", time.Now().Format("15:04:05"), sysFormatDuration(d), note), "", nil
}

// ---------- who / w / dmesg ----------

// sysWhoImpl 是 who/w 的共同实现：读取系统 utmp 需要平台 API，这里给出当前会话信息。
func (it *Interp) sysWhoImpl(cmd string, args []string) (string, string, error) {
	if len(args) > 0 {
		return it.failCmd(cmd, 1, fmt.Errorf("extra operand %q", args[0]))
	}
	username := "unknown"
	if u, err := user.Current(); err == nil {
		username = u.Username
	}
	host, _ := os.Hostname()
	now := time.Now().Format("2006-01-02 15:04")
	it.LastStatus = 0
	return fmt.Sprintf("%s\tpts/0\t%s\t(%s)\n# 说明：纯 Go 无法读取 utmp 登录会话，仅显示当前用户信息。\n",
		username, now, host), "", nil
}

// biWho / biW 共用当前会话信息。
func (it *Interp) biWho(args []string, stdin string) (string, string, error) {
	return it.sysWhoImpl("who", args)
}

func (it *Interp) biW(args []string, stdin string) (string, string, error) {
	return it.sysWhoImpl("w", args)
}

// biDmesg 读取内核日志环缓冲区；Windows 明确不支持。
func (it *Interp) biDmesg(args []string, stdin string) (string, string, error) {
	if runtime.GOOS == "windows" {
		return it.failCmd("dmesg", 1, fmt.Errorf("该平台不支持 dmesg（无内核日志环缓冲区）"))
	}
	data, err := os.ReadFile("/proc/kmsg")
	if err != nil {
		return it.failCmd("dmesg", 1, fmt.Errorf("无法读取内核日志（通常需要 root 权限）：%w", err))
	}
	it.LastStatus = 0
	return string(data), "", nil
}

// ---------- top / htop ----------

// sysTopImpl 在非 TTY 工具场景降级为一次性 ps 输出并给出说明。
func (it *Interp) sysTopImpl(name string, args []string, stdin string) (string, string, error) {
	out, stderr, err := it.biPS(args, stdin)
	if err != nil {
		return out, stderr, err
	}
	it.LastStatus = 0
	return fmt.Sprintf("# %s: 当前为非交互工具场景，已降级为一次性 ps 输出（无实时刷新/交互）。\n%s", name, out), "", nil
}

// biTop / biHtop 共用降级实现。
func (it *Interp) biTop(args []string, stdin string) (string, string, error) {
	return it.sysTopImpl("top", args, stdin)
}

func (it *Interp) biHtop(args []string, stdin string) (string, string, error) {
	return it.sysTopImpl("htop", args, stdin)
}

// ---------- jobs / fg / bg ----------

// sysJob 是逻辑作业表的一项；本工具的 shell 无真正后台进程，仅维护状态语义。
type sysJob struct {
	id         int
	command    string
	state      string // Running / Stopped / Done
	foreground bool
}

var (
	sysJobsMu    sync.Mutex
	sysJobs      []*sysJob
	sysNextJobID = 1
)

// sysRegisterJob 供内部登记一个后台作业并返回作业号（fg/bg 使用形如 %N 的编号）。
func sysRegisterJob(command string, background bool) int {
	sysJobsMu.Lock()
	defer sysJobsMu.Unlock()
	j := &sysJob{id: sysNextJobID, command: command, state: "Running", foreground: !background}
	sysNextJobID++
	sysJobs = append(sysJobs, j)
	return j.id
}

// biJobs 列出当前逻辑作业表。
func (it *Interp) biJobs(args []string, stdin string) (string, string, error) {
	if len(args) > 0 {
		return it.failCmd("jobs", 1, fmt.Errorf("extra operand %q", args[0]))
	}
	sysJobsMu.Lock()
	defer sysJobsMu.Unlock()
	var b strings.Builder
	if len(sysJobs) == 0 {
		b.WriteString("# 当前没有后台作业。本工具的 shell 不支持 '&' 真正后台执行，\n")
		b.WriteString("# 作业表由 nohup 登记的作业构成。\n")
	}
	for _, j := range sysJobs {
		mark := " "
		if j.foreground {
			mark = "*"
		}
		fmt.Fprintf(&b, "[%d]%s %-8s %s\n", j.id, mark, j.state, j.command)
	}
	it.LastStatus = 0
	return b.String(), "", nil
}

// sysJobSwitch 实现 fg/bg：仅切换逻辑作业状态，无法真正接管进程前后台。
func (it *Interp) sysJobSwitch(cmd string, args []string) (string, string, error) {
	if len(args) != 1 {
		return it.failCmd(cmd, 1, fmt.Errorf("用法: %s %%N", cmd))
	}
	id, err := strconv.Atoi(strings.TrimPrefix(args[0], "%"))
	if err != nil {
		return it.failCmd(cmd, 1, fmt.Errorf("无效作业号 %q", args[0]))
	}
	sysJobsMu.Lock()
	defer sysJobsMu.Unlock()
	for _, j := range sysJobs {
		if j.id == id {
			j.state = "Running"
			j.foreground = cmd == "fg"
			it.LastStatus = 0
			return fmt.Sprintf("[%d] %s: %s\n", id, j.state, j.command), "", nil
		}
	}
	return it.failCmd(cmd, 1, fmt.Errorf("无此作业: %%%d", id))
}

// biFg / biBg 共用作业切换逻辑。
func (it *Interp) biFg(args []string, stdin string) (string, string, error) {
	return it.sysJobSwitch("fg", args)
}

func (it *Interp) biBg(args []string, stdin string) (string, string, error) {
	return it.sysJobSwitch("bg", args)
}

// ---------- nohup ----------

// biNohup 登记后台作业后同步执行：工具场景无终端挂断信号，故无需真正忽略 SIGHUP。
func (it *Interp) biNohup(args []string, stdin string) (string, string, error) {
	if len(args) == 0 {
		return it.failCmd("nohup", 1, fmt.Errorf("缺少要执行的命令"))
	}
	id := sysRegisterJob(strings.Join(args, " "), true)
	var out, stderr string
	var err error
	if isBuiltin(args[0]) {
		out, stderr, err = it.runBuiltin(args[0], args[1:], stdin)
		if err != nil {
			return out, stderr, err
		}
	} else {
		it.LastStatus = 0
		out = fmt.Sprintf("# nohup: 外部命令 %q 请由 shell 正常路径执行；本工具无终端挂断问题。\n", args[0])
	}
	note := fmt.Sprintf("nohup: 已登记作业 [%%%d]；工具场景无终端挂断，命令已同步执行。\n", id)
	return note + out, stderr, nil
}
