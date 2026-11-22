package bash

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 本文件提供纯 Go 的网络类内建命令：不调用外部程序、不依赖 PATH。
// 所有网络请求都走带超时的上下文，避免无网环境下挂死解释器。

// netTimeout 把 it.Timeout 归一到默认 10s，保证任何网络操作都有上界。
func netTimeout(d time.Duration) time.Duration {
	if d <= 0 {
		return 10 * time.Second
	}
	return d
}

// netCtx 基于解释器上下文派生带超时的子上下文。
func (it *Interp) netCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(it.ctx(), netTimeout(it.Timeout))
}

// biCurl 实现 curl 的 GET/HEAD 子集：-I 只回显响应头，-o 写文件，-L 跟随重定向。
// 说明：http.Client 默认跟随重定向，故 -L 由默认行为天然满足（这里仅接收该标志）。
func (it *Interp) biCurl(args []string, stdin string) (string, string, error) {
	p, err := parseShortFlags(args, "IoL", "o")
	if err != nil {
		return it.failCmd("curl", 2, err)
	}
	if len(p.operands) != 1 {
		return it.failCmd("curl", 2, fmt.Errorf("需要一个 URL 参数"))
	}
	rawURL := p.operands[0]
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return it.failCmd("curl", 2, fmt.Errorf("URL 无效: %s", rawURL))
	}
	method := http.MethodGet
	if hasFlag(p, 'I') {
		method = http.MethodHead
	}
	ctx, cancel := it.netCtx()
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
	if err != nil {
		return it.failCmd("curl", 1, err)
	}
	req.Header.Set("User-Agent", "curl/8.0 (argentgo-shell)")
	client := &http.Client{Timeout: netTimeout(it.Timeout)}
	resp, err := client.Do(req)
	if err != nil {
		return it.failCmd("curl", 1, err)
	}
	defer resp.Body.Close()
	// -I：按 curl 格式输出状态行与响应头；键排序保证输出稳定可测。
	if hasFlag(p, 'I') {
		var b strings.Builder
		fmt.Fprintf(&b, "%s %s\n", resp.Proto, resp.Status)
		keys := make([]string, 0, len(resp.Header))
		for k := range resp.Header {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			for _, v := range resp.Header[k] {
				fmt.Fprintf(&b, "%s: %s\n", k, v)
			}
		}
		it.LastStatus = 0
		return b.String(), "", nil
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return it.failCmd("curl", 1, err)
	}
	if hasFlag(p, 'o') {
		abs, err := it.resolvePath(p.flags['o']) // 输出目标同样受工作区边界约束
		if err != nil {
			return it.failCmd("curl", 1, err)
		}
		if err := os.WriteFile(abs, body, 0o644); err != nil {
			return it.failCmd("curl", 1, err)
		}
		it.LastStatus = 0
		return "", "", nil
	}
	it.LastStatus = 0
	return string(body), "", nil
}

// biWget 实现 wget 的单文件下载子集：-O 指定文件名，-q 静默，默认从 URL 取文件名。
func (it *Interp) biWget(args []string, stdin string) (string, string, error) {
	p, err := parseShortFlags(args, "Oq", "O")
	if err != nil {
		return it.failCmd("wget", 2, err)
	}
	if len(p.operands) != 1 {
		return it.failCmd("wget", 2, fmt.Errorf("需要一个 URL 参数"))
	}
	rawURL := p.operands[0]
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return it.failCmd("wget", 2, fmt.Errorf("URL 无效: %s", rawURL))
	}
	name := p.flags['O']
	if !hasFlag(p, 'O') {
		name = path.Base(u.Path) // URL 路径用 / 分隔，path 包不随宿主系统变化
		if name == "." || name == "/" || name == "" {
			name = "index.html"
		}
	}
	ctx, cancel := it.netCtx()
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return it.failCmd("wget", 1, err)
	}
	req.Header.Set("User-Agent", "Wget/1.21 (argentgo-shell)")
	client := &http.Client{Timeout: netTimeout(it.Timeout)}
	resp, err := client.Do(req)
	if err != nil {
		return it.failCmd("wget", 1, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return it.failCmd("wget", 1, err)
	}
	abs, err := it.resolvePath(name)
	if err != nil {
		return it.failCmd("wget", 1, err)
	}
	if err := os.WriteFile(abs, body, 0o644); err != nil {
		return it.failCmd("wget", 1, err)
	}
	it.LastStatus = 0
	if hasFlag(p, 'q') {
		return "", "", nil
	}
	// wget 的进度信息走 stderr，这里给出一行简明的保存提示。
	return "", fmt.Sprintf("已保存 [%s] (%d 字节)\n", name, len(body)), nil
}

// biPing 用 TCP :80 拨号近似 ping：标准库没有 ICMP 实现，故输出中明确声明这是近似探测。
func (it *Interp) biPing(args []string, stdin string) (string, string, error) {
	p, err := parseShortFlags(args, "c", "c")
	if err != nil {
		return it.failCmd("ping", 2, err)
	}
	n := 3
	if hasFlag(p, 'c') {
		n, err = strconv.Atoi(p.flags['c'])
		if err != nil || n < 1 {
			return it.failCmd("ping", 2, fmt.Errorf("无效的探测次数: %q", p.flags['c']))
		}
	}
	if len(p.operands) != 1 {
		return it.failCmd("ping", 2, fmt.Errorf("需要一个主机名或 IP"))
	}
	host := p.operands[0]
	target := host
	if _, _, e := net.SplitHostPort(host); e != nil {
		target = net.JoinHostPort(host, "80")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "注意：当前为 TCP :80 连通性探测（近似 ping），非 ICMP。\n")
	fmt.Fprintf(&b, "PING %s (%s)\n", host, target)
	dialer := &net.Dialer{Timeout: netTimeout(it.Timeout)}
	var rtts []time.Duration
	for i := 0; i < n; i++ {
		ctx, cancel := it.netCtx()
		start := time.Now()
		conn, e := dialer.DialContext(ctx, "tcp", target)
		elapsed := time.Since(start)
		cancel()
		if e != nil {
			fmt.Fprintf(&b, "probe seq=%d: 失败 (%v)\n", i, e)
			continue
		}
		conn.Close()
		rtts = append(rtts, elapsed)
		fmt.Fprintf(&b, "%s: seq=%d time=%.3f ms\n", target, i, float64(elapsed.Microseconds())/1000)
	}
	fmt.Fprintf(&b, "\n--- %s ping statistics ---\n", host)
	fmt.Fprintf(&b, "%d probes transmitted, %d succeeded, %d%% failure\n",
		n, len(rtts), (n-len(rtts))*100/n)
	if len(rtts) > 0 {
		min, max, sum := rtts[0], rtts[0], time.Duration(0)
		for _, r := range rtts {
			if r < min {
				min = r
			}
			if r > max {
				max = r
			}
			sum += r
		}
		avg := sum / time.Duration(len(rtts))
		fmt.Fprintf(&b, "round-trip min/avg/max = %.3f/%.3f/%.3f ms\n",
			float64(min.Microseconds())/1000, float64(avg.Microseconds())/1000, float64(max.Microseconds())/1000)
		it.LastStatus = 0
	} else {
		it.LastStatus = 1
	}
	return b.String(), "", nil
}

// socketRow 是 /proc/net/tcp 解析出的一个监听项。
type socketRow struct {
	proto         string
	local, remote string
	state         string
	inode         string
	sendQ, recvQ  uint64
}

// procAddr 把 /proc 中的 "十六进制地址:十六进制端口" 转为可读地址。
// /proc 的地址按 32 位字以本机字节序书写，需要逐字节还原。
func procAddr(s string) string {
	i := strings.LastIndexByte(s, ':')
	if i < 0 {
		return s
	}
	ipHex, portHex := s[:i], s[i+1:]
	port, _ := strconv.ParseUint(portHex, 16, 16)
	if len(ipHex) == 8 {
		v, _ := strconv.ParseUint(ipHex, 16, 32)
		return fmt.Sprintf("%d.%d.%d.%d:%d", byte(v), byte(v>>8), byte(v>>16), byte(v>>24), port)
	}
	if len(ipHex) == 32 {
		var raw []byte
		for j := 0; j < len(ipHex); j += 8 {
			v, _ := strconv.ParseUint(ipHex[j:j+8], 16, 32)
			raw = append(raw, byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
		}
		return fmt.Sprintf("[%s]:%d", net.IP(raw).String(), port)
	}
	return s
}

// procStateName 把 /proc 的状态码映射为 netstat 风格的名字。
func procStateName(hexCode, proto string) string {
	if proto == "udp" {
		return "" // UDP 无连接状态
	}
	switch hexCode {
	case "0A":
		return "LISTEN"
	case "01":
		return "ESTABLISHED"
	case "02":
		return "SYN_SENT"
	case "03":
		return "SYN_RECV"
	case "04":
		return "FIN_WAIT1"
	case "05":
		return "FIN_WAIT2"
	case "06":
		return "TIME_WAIT"
	case "07":
		return "CLOSE"
	case "08":
		return "CLOSE_WAIT"
	case "09":
		return "LAST_ACK"
	default:
		return "UNKNOWN"
	}
}

// parseProcNetFile 解析单个 /proc/net/tcp* 或 udp* 文件，只保留监听项。
func parseProcNetFile(file, proto string) []socketRow {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var rows []socketRow
	for _, line := range lines[1:] { // 跳过表头
		f := strings.Fields(line)
		if len(f) < 10 {
			continue
		}
		if proto == "tcp" && f[3] != "0A" {
			continue
		}
		q := strings.Split(f[4], ":")
		var sendQ, recvQ uint64
		if len(q) == 2 {
			sendQ, _ = strconv.ParseUint(q[0], 16, 64)
			recvQ, _ = strconv.ParseUint(q[1], 16, 64)
		}
		rows = append(rows, socketRow{
			proto:  proto,
			local:  procAddr(f[1]),
			remote: procAddr(f[2]),
			state:  procStateName(f[3], proto),
			inode:  f[9],
			sendQ:  sendQ,
			recvQ:  recvQ,
		})
	}
	return rows
}

// procInodePIDs 尽力把 socket inode 映射到 pid/进程名；无权限时返回空表。
func procInodePIDs() map[string]string {
	m := make(map[string]string)
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return m
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid := e.Name()
		if _, err := strconv.Atoi(pid); err != nil {
			continue
		}
		fds, err := os.ReadDir("/proc/" + pid + "/fd")
		if err != nil {
			continue
		}
		for _, fd := range fds {
			link, err := os.Readlink("/proc/" + pid + "/fd/" + fd.Name())
			if err != nil {
				continue
			}
			const prefix = "socket:["
			if !strings.HasPrefix(link, prefix) || !strings.HasSuffix(link, "]") {
				continue
			}
			ino := strings.TrimSuffix(strings.TrimPrefix(link, prefix), "]")
			if _, exists := m[ino]; exists {
				continue
			}
			comm, _ := os.ReadFile("/proc/" + pid + "/comm")
			m[ino] = pid + "/" + strings.TrimSpace(string(comm))
		}
	}
	return m
}

// netSocketRows 返回本机监听项。stdlib 无法枚举端口，故 Linux 读 /proc，
// 其它平台给出明确中文错误（Windows 需 GetExtendedTcpTable，受单文件/无 cgo 约束未内建）。
func (it *Interp) netSocketRows(showTCP, showUDP bool) ([]socketRow, error) {
	if runtime.GOOS != "linux" {
		return nil, fmt.Errorf("当前平台不支持枚举监听端口：仅 Linux /proc/net/tcp 可用（Windows 需 GetExtendedTcpTable）；请用工具层的 process 执行 netstat/ss")
	}
	var rows []socketRow
	if showTCP {
		for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
			rows = append(rows, parseProcNetFile(f, "tcp")...)
		}
	}
	if showUDP {
		for _, f := range []string{"/proc/net/udp", "/proc/net/udp6"} {
			rows = append(rows, parseProcNetFile(f, "udp")...)
		}
	}
	if rows == nil {
		return nil, fmt.Errorf("无法读取 /proc/net 下的套接字表（可能无权限或文件不存在）")
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].local < rows[j].local })
	return rows, nil
}

// netSockets 是 ss / netstat 的共同实现。
// -n（数字地址）与 -l（仅监听）是这里唯一可行的输出形态，故自动满足；
// -t/-u 选择协议，-p 尽力显示进程。
func (it *Interp) netSockets(name string, args []string, stdin string) (string, string, error) {
	p, err := parseShortFlags(args, "tulnp", "")
	if err != nil {
		return it.failCmd(name, 2, err)
	}
	showTCP := hasFlag(p, 't') || (!hasFlag(p, 't') && !hasFlag(p, 'u'))
	showUDP := hasFlag(p, 'u') || (!hasFlag(p, 't') && !hasFlag(p, 'u'))
	rows, err := it.netSocketRows(showTCP, showUDP)
	if err != nil {
		return it.failCmd(name, 1, err)
	}
	var procs map[string]string
	if hasFlag(p, 'p') {
		procs = procInodePIDs()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%-6s %-26s %-26s %-12s %s\n", "Proto", "Local Address", "Foreign Address", "State", "PID/Program")
	for _, r := range rows {
		proc := "-"
		if v, ok := procs[r.inode]; ok {
			proc = v
		}
		fmt.Fprintf(&b, "%-6s %-26s %-26s %-12s %s\n", r.proto, r.local, r.remote, r.state, proc)
	}
	if len(rows) == 0 {
		b.WriteString("（未发现监听套接字）\n")
	}
	it.LastStatus = 0
	return b.String(), "", nil
}

func (it *Interp) biSs(args []string, stdin string) (string, string, error) {
	return it.netSockets("ss", args, stdin)
}

func (it *Interp) biNetstat(args []string, stdin string) (string, string, error) {
	return it.netSockets("netstat", args, stdin)
}

// netIface 是单个网卡的展示数据，由 net.Interfaces/Addrs 汇总。
type netIface struct {
	name, flags, hw string
	mtu             int
	addrs           []netAddrInfo
}

type netAddrInfo struct {
	family, addr, scope string
}

// collectNetIfaces 汇总网卡信息并按名字排序，供 ip / ifconfig 共用。
func collectNetIfaces() ([]netIface, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []netIface
	for _, in := range ifs {
		var fl []string
		if in.Flags&net.FlagUp != 0 {
			fl = append(fl, "UP")
		}
		if in.Flags&net.FlagLoopback != 0 {
			fl = append(fl, "LOOPBACK")
		}
		if in.Flags&net.FlagBroadcast != 0 {
			fl = append(fl, "BROADCAST")
		}
		if in.Flags&net.FlagMulticast != 0 {
			fl = append(fl, "MULTICAST")
		}
		if in.Flags&net.FlagPointToPoint != 0 {
			fl = append(fl, "POINTOPOINT")
		}
		if len(fl) == 0 {
			fl = []string{"DOWN"}
		}
		item := netIface{name: in.Name, flags: strings.Join(fl, ","), mtu: in.MTU, hw: in.HardwareAddr.String()}
		addrs, err := in.Addrs()
		if err == nil {
			for _, a := range addrs {
				info := netAddrInfo{family: "inet", addr: a.String()}
				if ipn, ok := a.(*net.IPNet); ok && ipn.IP != nil {
					if ipn.IP.To4() == nil {
						info.family = "inet6"
					}
					switch {
					case ipn.IP.IsLoopback():
						info.scope = "host"
					case ipn.IP.IsLinkLocalUnicast():
						info.scope = "link"
					default:
						info.scope = "global"
					}
				}
				item.addrs = append(item.addrs, info)
			}
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}

func ifaceState(flags string) string {
	if strings.Contains(flags, "UP") {
		return "UP"
	}
	return "DOWN"
}

// biIp 输出 ip addr 风格的地址信息；接受 -4/-6 过滤，其余参数忽略。
func (it *Interp) biIp(args []string, stdin string) (string, string, error) {
	only4, only6 := false, false
	for _, a := range args {
		switch a {
		case "-4":
			only4 = true
		case "-6":
			only6 = true
		}
	}
	ifaces, err := collectNetIfaces()
	if err != nil {
		return it.failCmd("ip", 1, err)
	}
	var b strings.Builder
	for i, in := range ifaces {
		fmt.Fprintf(&b, "%d: %s: <%s> mtu %d state %s\n", i+1, in.name, in.flags, in.mtu, ifaceState(in.flags))
		if in.hw != "" {
			kind := "link/ether"
			if strings.Contains(in.flags, "LOOPBACK") {
				kind = "link/loopback"
			}
			fmt.Fprintf(&b, "    %s %s\n", kind, in.hw)
		}
		for _, a := range in.addrs {
			if only4 && a.family != "inet" {
				continue
			}
			if only6 && a.family != "inet6" {
				continue
			}
			scope := ""
			if a.scope != "" {
				scope = " scope " + a.scope
			}
			fmt.Fprintf(&b, "    %s %s%s\n", a.family, a.addr, scope)
		}
	}
	it.LastStatus = 0
	return b.String(), "", nil
}

// biIfconfig 输出 ifconfig 风格的地址信息；带网卡名参数时只显示匹配项。
func (it *Interp) biIfconfig(args []string, stdin string) (string, string, error) {
	ifaces, err := collectNetIfaces()
	if err != nil {
		return it.failCmd("ifconfig", 1, err)
	}
	var b strings.Builder
	for _, in := range ifaces {
		if len(args) > 0 && !containsString(args, in.name) {
			continue
		}
		fmt.Fprintf(&b, "%s: flags=%s mtu %d\n", in.name, in.flags, in.mtu)
		for _, a := range in.addrs {
			if a.scope != "" {
				fmt.Fprintf(&b, "        %s %s  scope %s\n", a.family, a.addr, a.scope)
			} else {
				fmt.Fprintf(&b, "        %s %s\n", a.family, a.addr)
			}
		}
		if in.hw != "" {
			fmt.Fprintf(&b, "        ether %s\n", in.hw)
		}
		b.WriteString("\n")
	}
	it.LastStatus = 0
	return b.String(), "", nil
}

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// netDNSLookup 用默认解析器查询 A/AAAA 与 CNAME；CNAME 不存在不算整体失败。
func (it *Interp) netDNSLookup(domain string) (string, []net.IP, error) {
	ctx, cancel := it.netCtx()
	defer cancel()
	r := net.DefaultResolver
	ips, err := r.LookupIP(ctx, "ip", domain) // "ip" 同时返回 IPv4 与 IPv6
	if err != nil {
		return "", nil, err
	}
	cname, _ := r.LookupCNAME(ctx, domain) // 无 CNAME 时忽略错误
	return cname, ips, nil
}

// firstOperand 跳过前导选项，返回第一个域名参数。
func firstOperand(args []string) string {
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			return a
		}
	}
	return ""
}

// biDig 输出 dig 风格的 A/AAAA 与 CNAME 记录。
func (it *Interp) biDig(args []string, stdin string) (string, string, error) {
	domain := firstOperand(args)
	if domain == "" {
		return it.failCmd("dig", 2, fmt.Errorf("需要一个域名"))
	}
	cname, ips, err := it.netDNSLookup(domain)
	if err != nil {
		return it.failCmd("dig", 1, err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n; <<>> argentgo dig <<>> %s\n\n", domain)
	b.WriteString(";; ANSWER SECTION:\n")
	for _, ip := range ips {
		typ := "A"
		if ip.To4() == nil {
			typ = "AAAA"
		}
		fmt.Fprintf(&b, "%s.  0  IN  %s  %s\n", domain, typ, ip.String())
	}
	if cname != "" && cname != domain+"." {
		fmt.Fprintf(&b, "%s.  0  IN  CNAME  %s\n", domain, cname)
	}
	it.LastStatus = 0
	return b.String(), "", nil
}

// biNslookup 输出 nslookup 风格的域名解析结果。
func (it *Interp) biNslookup(args []string, stdin string) (string, string, error) {
	domain := firstOperand(args)
	if domain == "" {
		return it.failCmd("nslookup", 2, fmt.Errorf("需要一个域名"))
	}
	cname, ips, err := it.netDNSLookup(domain)
	if err != nil {
		return it.failCmd("nslookup", 1, err)
	}
	var b strings.Builder
	b.WriteString("Server:  system-default\n")
	b.WriteString("Address: 127.0.0.1#53\n\n")
	b.WriteString("Non-authoritative answer:\n")
	fmt.Fprintf(&b, "Name:    %s\n", domain)
	if len(ips) > 0 {
		b.WriteString("Addresses:\n")
		for _, ip := range ips {
			fmt.Fprintf(&b, "  %s\n", ip.String())
		}
	}
	if cname != "" && cname != domain+"." {
		fmt.Fprintf(&b, "Aliases: %s\n", cname)
	}
	it.LastStatus = 0
	return b.String(), "", nil
}

// unsupportedSSH 统一报告需要完整 SSH 协议、标准库无法实现的命令。
func (it *Interp) unsupportedSSH(name string) (string, string, error) {
	return it.failCmd(name, 127, fmt.Errorf("该命令需要外部 SSH 实现，当前内建不支持，请用工具层的 process 执行"))
}

func (it *Interp) biSsh(args []string, stdin string) (string, string, error) {
	return it.unsupportedSSH("ssh")
}

func (it *Interp) biScp(args []string, stdin string) (string, string, error) {
	return it.unsupportedSSH("scp")
}

func (it *Interp) biRsync(args []string, stdin string) (string, string, error) {
	return it.unsupportedSSH("rsync")
}
