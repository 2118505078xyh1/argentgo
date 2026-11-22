package bash

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// 本文件补齐一批纯 Go 文本处理内建：sed/awk/xargs/sort/uniq/cut/tr/less/more/nl。
// 全部复用包内已有的 resolvePath/readFileArg/collectTextInputs，因此文件读取
// 一律受工作区边界约束；不调用任何外部程序，也不依赖 PATH。

// ---------------------------- sed ----------------------------

// sedAddr 是 sed 的行地址：start/end 为 1 基，-1 表示 $（末行），end 为 0 表示单地址。
type sedAddr struct {
	start int
	end   int
}

func (a sedAddr) matches(n, total int) bool {
	resolve := func(v int) int {
		if v == -1 {
			return total
		}
		return v
	}
	start := resolve(a.start)
	if a.end == 0 {
		return n == start
	}
	return n >= start && n <= resolve(a.end)
}

// sedCmd 是解析后的一条 sed 子命令：替换（s///）或打印（p）。
type sedCmd struct {
	addr       sedAddr
	subst      bool
	print      bool // p 命令
	substPrint bool // s///p 标志
	re         *regexp.Regexp
	repl       string
	global     bool
	pattern    string // 用于 i 标志重编译
}

// sedParseAddr 读取行号/范围前缀；无地址时原样返回输入。
func sedParseAddr(s string) (sedAddr, string, error) {
	readNum := func(s string) (int, string, bool) {
		if len(s) > 0 && s[0] == '$' {
			return -1, s[1:], true
		}
		i := 0
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		if i == 0 {
			return 0, s, false
		}
		n, _ := strconv.Atoi(s[:i])
		return n, s[i:], true
	}
	start, rest, ok := readNum(s)
	if !ok {
		return sedAddr{}, s, nil
	}
	a := sedAddr{start: start}
	tail := strings.TrimLeft(rest, " \t")
	if strings.HasPrefix(tail, ",") {
		end, after, ok2 := readNum(strings.TrimLeft(tail[1:], " \t"))
		if !ok2 {
			return sedAddr{}, s, fmt.Errorf("sed: 无效的地址范围 %q", tail)
		}
		a.end = end
		rest = after
	}
	return a, rest, nil
}

// sedScanField 读取以 delim 分隔的字段，兼容 \<delim> 转义，返回内容与剩余文本。
func sedScanField(s string, delim byte) (string, string, bool) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' && i+1 < len(s) {
			next := s[i+1]
			if next == delim {
				b.WriteByte(delim)
			} else {
				b.WriteByte(c)
				b.WriteByte(next)
			}
			i++
			continue
		}
		if c == delim {
			return b.String(), s[i+1:], true
		}
		b.WriteByte(c)
	}
	return "", "", false
}

// sedParseSubst 解析 s/pat/repl/flags，s 之后的空白允许出现。
func sedParseSubst(s string) (sedCmd, string, error) {
	var c sedCmd
	tail := strings.TrimLeft(s[1:], " \t")
	if tail == "" {
		return c, s, fmt.Errorf("sed: s 命令缺少分隔符")
	}
	delim := tail[0]
	pat, rest, ok := sedScanField(tail[1:], delim)
	if !ok {
		return c, s, fmt.Errorf("sed: 未闭合的替换模式")
	}
	repl, rest, ok := sedScanField(rest, delim)
	if !ok {
		return c, s, fmt.Errorf("sed: 未闭合的替换文本")
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		return c, s, fmt.Errorf("sed: 无效正则 %q: %w", pat, err)
	}
	c = sedCmd{subst: true, re: re, repl: repl, pattern: pat}
	// 标志仅取连续可识别字符，遇到其他字符即停，交回主循环当命令解析。
	flags := rest
	parsed := true
	for parsed && len(flags) > 0 {
		switch flags[0] {
		case 'g':
			c.global = true
		case 'i':
			re, err = regexp.Compile("(?i)" + pat)
			if err != nil {
				return c, s, fmt.Errorf("sed: 无效正则 %q: %w", pat, err)
			}
			c.re = re
		case 'p':
			c.substPrint = true
		default:
			parsed = false
			continue
		}
		flags = flags[1:]
	}
	return c, flags, nil
}

// parseSedScript 顺序解析脚本，支持 ; 与换行分隔、N / N,M / $ 地址、
// s///（g/i/p 标志）与 p 命令；其余命令明确报错。
func parseSedScript(script string) ([]sedCmd, error) {
	var cmds []sedCmd
	s := strings.TrimSpace(script)
	for s != "" {
		addr, rest, err := sedParseAddr(s)
		if err != nil {
			return nil, err
		}
		rest = strings.TrimLeft(rest, " \t")
		if rest == "" {
			return nil, fmt.Errorf("sed: 地址后缺少命令")
		}
		var c sedCmd
		switch rest[0] {
		case 'p':
			c = sedCmd{print: true}
			rest = rest[1:]
		case 's':
			c, rest, err = sedParseSubst(rest)
			if err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("sed: 不支持的命令 %q（仅支持 s/// 与 p）", string(rest[0]))
		}
		c.addr = addr
		cmds = append(cmds, c)
		s = strings.TrimLeft(rest, " \t;\n")
	}
	return cmds, nil
}

// sedExpandRepl 展开替换文本中的 &（整段匹配）与 \0..\9（子匹配）。
func sedExpandRepl(line string, loc []int, repl string) string {
	var b strings.Builder
	for i := 0; i < len(repl); i++ {
		c := repl[i]
		if c == '\\' && i+1 < len(repl) {
			n := repl[i+1]
			if n >= '0' && n <= '9' {
				k := int(n-'0') * 2
				if k+1 < len(loc) && loc[k] >= 0 {
					b.WriteString(line[loc[k]:loc[k+1]])
				}
				i++
				continue
			}
			if n == '&' || n == '\\' {
				b.WriteByte(n)
				i++
				continue
			}
		}
		if c == '&' {
			b.WriteString(line[loc[0]:loc[1]])
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

func sedApply(c sedCmd, line string) string {
	var all [][]int
	if c.global {
		all = c.re.FindAllStringSubmatchIndex(line, -1)
	} else if loc := c.re.FindStringSubmatchIndex(line); loc != nil {
		all = [][]int{loc}
	}
	if len(all) == 0 {
		return line
	}
	var b strings.Builder
	last := 0
	for _, loc := range all {
		if loc[0] < last {
			continue
		}
		b.WriteString(line[last:loc[0]])
		b.WriteString(sedExpandRepl(line, loc, c.repl))
		last = loc[1]
	}
	b.WriteString(line[last:])
	return b.String()
}

// biSed 实现 sed 子集。-i 原地写被明确拒绝，需改用工具层文件编辑能力。
func (it *Interp) biSed(args []string, stdin string) (string, string, error) {
	for _, a := range args {
		if a == "-i" || strings.HasPrefix(a, "-i") || a == "--in-place" {
			return it.failCmd("sed", 2, fmt.Errorf("-i 原地写不受支持，请改用工具层的文件编辑能力（如 edit/patch）"))
		}
	}
	p, err := parseShortFlags(args, "n", "")
	if err != nil {
		return it.failCmd("sed", 2, err)
	}
	if len(p.operands) == 0 {
		return it.failCmd("sed", 2, fmt.Errorf("missing script"))
	}
	cmds, err := parseSedScript(p.operands[0])
	if err != nil {
		return it.failCmd("sed", 1, err)
	}
	inputs, err := it.collectTextInputs(p.operands[1:], stdin, "sed")
	if err != nil {
		return it.failCmd("sed", 1, err)
	}
	var stream strings.Builder
	for _, in := range inputs {
		stream.WriteString(in.data)
	}
	data := stream.String()
	lines := splitLines(data)
	quiet := hasFlag(p, 'n')
	var out []string
	for i, line := range lines {
		n, pattern := i+1, line
		for _, c := range cmds {
			if c.addr.start != 0 && !c.addr.matches(n, len(lines)) {
				continue
			}
			switch {
			case c.subst:
				next := sedApply(c, pattern)
				if next != pattern && c.substPrint {
					out = append(out, next)
				}
				pattern = next
			case c.print:
				out = append(out, pattern)
			}
		}
		if !quiet {
			out = append(out, pattern)
		}
	}
	it.LastStatus = 0
	if len(out) == 0 {
		return "", "", nil
	}
	result := strings.Join(out, "\n")
	if strings.HasSuffix(data, "\n") {
		result += "\n"
	}
	return result, "", nil
}

// ---------------------------- awk ----------------------------

type awkItem struct {
	kind int // 0:$N 1:$0 2:NR 3:NF 4:字符串字面量
	idx  int
	text string
}

type awkCmp struct {
	op    string
	value int
}

type awkProg struct {
	patternRe *regexp.Regexp
	nrCmp     *awkCmp
	items     []awkItem
}

func (ap awkProg) match(line string, nr int) bool {
	if ap.patternRe != nil && !ap.patternRe.MatchString(line) {
		return false
	}
	if ap.nrCmp != nil {
		v := ap.nrCmp.value
		switch ap.nrCmp.op {
		case "==":
			if nr != v {
				return false
			}
		case "!=":
			if nr == v {
				return false
			}
		case ">":
			if nr <= v {
				return false
			}
		case ">=":
			if nr < v {
				return false
			}
		case "<":
			if nr >= v {
				return false
			}
		case "<=":
			if nr > v {
				return false
			}
		}
	}
	return true
}

func parseAwkNrCmp(s string) (*awkCmp, error) {
	if s == "NR" {
		return &awkCmp{op: "!=", value: 0}, nil
	}
	for _, op := range []string{"==", "!=", ">=", "<=", ">", "<"} {
		if i := strings.Index(s, op); i >= 0 {
			n, err := strconv.Atoi(strings.TrimSpace(s[i+len(op):]))
			if err != nil {
				return nil, fmt.Errorf("awk: NR 比较需要整数: %q", s)
			}
			return &awkCmp{op: op, value: n}, nil
		}
	}
	return nil, fmt.Errorf("awk: 不支持的 NR 模式 %q", s)
}

func parseAwkItem(f string) (awkItem, error) {
	switch {
	case f == "$0":
		return awkItem{kind: 1}, nil
	case len(f) > 1 && f[0] == '$' && isDigits(f[1:]):
		n, err := strconv.Atoi(f[1:])
		if err != nil || n < 1 {
			return awkItem{}, fmt.Errorf("awk: 无效字段 $%s", f[1:])
		}
		return awkItem{kind: 0, idx: n}, nil
	case f == "NR":
		return awkItem{kind: 2}, nil
	case f == "NF":
		return awkItem{kind: 3}, nil
	case len(f) >= 2 && (f[0] == '"' || f[0] == '\'') && f[len(f)-1] == f[0]:
		return awkItem{kind: 4, text: f[1 : len(f)-1]}, nil
	default:
		return awkItem{}, fmt.Errorf("awk: 不支持的打印表达式 %q（仅支持 $0/$N/NR/NF/字符串字面量）", f)
	}
}

// parseAwkProgram 只接受 /re/ 或 NR 比较作为可选模式，动作体只接受 print。
func parseAwkProgram(prog string) (awkProg, error) {
	var ap awkProg
	s := strings.TrimSpace(prog)
	brace := strings.IndexByte(s, '{')
	if brace < 0 {
		return ap, fmt.Errorf("awk: 仅支持 { print ... } 形式的动作")
	}
	pat, rest := strings.TrimSpace(s[:brace]), strings.TrimSpace(s[brace:])
	if !strings.HasSuffix(rest, "}") {
		return ap, fmt.Errorf("awk: 动作缺少 }")
	}
	action := strings.TrimSpace(rest[1 : len(rest)-1])
	if pat != "" {
		if strings.HasPrefix(pat, "/") && strings.LastIndexByte(pat, '/') > 0 {
			src := pat[1:strings.LastIndexByte(pat, '/')]
			re, err := regexp.Compile(src)
			if err != nil {
				return ap, fmt.Errorf("awk: 无效正则 %q: %w", src, err)
			}
			ap.patternRe = re
		} else if strings.HasPrefix(pat, "NR") {
			cmp, err := parseAwkNrCmp(pat)
			if err != nil {
				return ap, err
			}
			ap.nrCmp = cmp
		} else {
			return ap, fmt.Errorf("awk: 不支持的模式 %q（仅支持 /正则/ 与 NR 比较）", pat)
		}
	}
	if !strings.HasPrefix(action, "print") {
		return ap, fmt.Errorf("awk: 不支持的动作 %q（仅支持 print）", action)
	}
	expr := strings.TrimSuffix(strings.TrimSpace(strings.TrimPrefix(action, "print")), ";")
	if expr == "" {
		ap.items = []awkItem{{kind: 1}}
		return ap, nil
	}
	for _, f := range strings.Split(expr, ",") {
		item, err := parseAwkItem(strings.TrimSpace(f))
		if err != nil {
			return ap, err
		}
		ap.items = append(ap.items, item)
	}
	return ap, nil
}

// biAwk 实现 awk 最小子集：默认按空白分字段，-F 可指定分隔符；
// NR/NF/$0 与 $N 可用于 print，多文件之间 NR 连续累计。
func (it *Interp) biAwk(args []string, stdin string) (string, string, error) {
	p, err := parseShortFlags(args, "F", "F")
	if err != nil {
		return it.failCmd("awk", 2, err)
	}
	if len(p.operands) == 0 {
		return it.failCmd("awk", 2, fmt.Errorf("missing program"))
	}
	ap, err := parseAwkProgram(p.operands[0])
	if err != nil {
		return it.failCmd("awk", 2, err)
	}
	inputs, err := it.collectTextInputs(p.operands[1:], stdin, "awk")
	if err != nil {
		return it.failCmd("awk", 2, err)
	}
	nr, out := 0, []string(nil)
	for _, in := range inputs {
		for _, line := range splitLines(in.data) {
			nr++
			var fields []string
			if hasFlag(p, 'F') && p.flags['F'] != "" {
				fields = strings.Split(line, p.flags['F'])
			} else {
				fields = strings.Fields(line)
			}
			if !ap.match(line, nr) {
				continue
			}
			vals := make([]string, len(ap.items))
			for i, item := range ap.items {
				switch item.kind {
				case 0:
					if item.idx <= len(fields) {
						vals[i] = fields[item.idx-1]
					}
				case 1:
					vals[i] = line
				case 2:
					vals[i] = strconv.Itoa(nr)
				case 3:
					vals[i] = strconv.Itoa(len(fields))
				case 4:
					vals[i] = item.text
				}
			}
			out = append(out, strings.Join(vals, " "))
		}
	}
	it.LastStatus = 0
	if len(out) == 0 {
		return "", "", nil
	}
	return strings.Join(out, "\n") + "\n", "", nil
}

// ---------------------------- xargs ----------------------------

// biXargs 从 stdin 取词（默认）或按行（-I），分批追加到命令参数后，
// 用 CallExpr + execStmt 执行，因此内建/函数/外部命令与 shell 一致，
// 并经过同样的安全谓词与工作区约束。-I 保证每个参数作为单一字面词，不做分词/glob。
func (it *Interp) biXargs(args []string, stdin string) (string, string, error) {
	p, err := parseShortFlags(args, "nIr", "nI")
	if err != nil {
		return it.failCmd("xargs", 1, err)
	}
	ops := p.operands
	if len(ops) == 0 {
		return it.failCmd("xargs", 1, fmt.Errorf("missing command"))
	}
	it.LastStatus = 0 // 若没有任何批次执行（如 -I 空输入），保持成功状态
	n := 0
	if hasFlag(p, 'n') {
		n, err = strconv.Atoi(p.flags['n'])
		if err != nil || n < 1 {
			return it.failCmd("xargs", 1, fmt.Errorf("invalid -n value %q", p.flags['n']))
		}
	}
	var items []string
	if hasFlag(p, 'I') {
		items = splitLines(stdin)
	} else {
		items = strings.Fields(stdin)
	}
	if len(items) == 0 && hasFlag(p, 'r') {
		it.LastStatus = 0
		return "", "", nil
	}
	var batches [][]string
	if hasFlag(p, 'I') {
		repl := p.flags['I']
		for _, item := range items {
			batch := make([]string, len(ops))
			replaced := false
			for i, a := range ops {
				if repl != "" && strings.Contains(a, repl) {
					batch[i] = strings.ReplaceAll(a, repl, item)
					replaced = true
				} else {
					batch[i] = a
				}
			}
			if !replaced {
				batch = append(batch, item)
			}
			batches = append(batches, batch)
		}
	} else {
		size := len(items)
		if n > 0 {
			size = n
		}
		if len(items) == 0 {
			batches = [][]string{ops} // GNU 默认即便空输入也执行一次
		} else {
			for i := 0; i < len(items); i += size {
				end := i + size
				if end > len(items) {
					end = len(items)
				}
				batch := append([]string{}, ops...)
				batches = append(batches, append(batch, items[i:end]...))
			}
		}
	}
	var out, errout strings.Builder
	for _, batch := range batches {
		words := make([]*Word, 0, len(batch))
		for _, a := range batch {
			words = append(words, &Word{Parts: []WordPart{&SglQuotedPart{Text: a}}})
		}
		// execStmt 返回 (可见输出, 管道 stdout)。可见输出 = stdout+stderr（或重定向后的残余），
		// 故去掉管道 stdout 的前缀即可还原子命令的 stderr；无重定向时二者相等，差集为空。
		visible, pipeOut, err := it.execStmt(&Stmt{Cmd: &CallExpr{Args: words}}, "")
		out.WriteString(pipeOut)
		if strings.HasPrefix(visible, pipeOut) {
			errout.WriteString(strings.TrimPrefix(visible, pipeOut))
		}
		if err != nil {
			return out.String(), errout.String(), err
		}
	}
	return out.String(), errout.String(), nil
}

// ---------------------------- sort / uniq ----------------------------

func sortKeyEq(a, b string, numeric bool) bool {
	if numeric {
		fa, ea := strconv.ParseFloat(strings.TrimSpace(a), 64)
		fb, eb := strconv.ParseFloat(strings.TrimSpace(b), 64)
		if ea == nil && eb == nil && fa == fb {
			return true
		}
	}
	return a == b
}

// biSort 实现 sort 的 -r（逆序）/-n（数值）/-u（去重）组合，默认字典序。
func (it *Interp) biSort(args []string, stdin string) (string, string, error) {
	p, err := parseShortFlags(args, "rnu", "")
	if err != nil {
		return it.failCmd("sort", 2, err)
	}
	inputs, err := it.collectTextInputs(p.operands, stdin, "sort")
	if err != nil {
		return it.failCmd("sort", 1, err)
	}
	var lines []string
	for _, in := range inputs {
		lines = append(lines, splitLines(in.data)...)
	}
	numeric, reverse := hasFlag(p, 'n'), hasFlag(p, 'r')
	less := func(a, b string) bool {
		if numeric {
			fa, ea := strconv.ParseFloat(strings.TrimSpace(a), 64)
			fb, eb := strconv.ParseFloat(strings.TrimSpace(b), 64)
			if ea == nil && eb == nil {
				if fa != fb {
					return fa < fb
				}
				return a < b // 数值相同按整行作最后比较
			}
			if ea == nil {
				return true
			}
			if eb == nil {
				return false
			}
		}
		return a < b
	}
	sort.Slice(lines, func(i, j int) bool {
		if reverse {
			return less(lines[j], lines[i])
		}
		return less(lines[i], lines[j])
	})
	if hasFlag(p, 'u') {
		kept := lines[:0]
		for _, l := range lines {
			if len(kept) > 0 && sortKeyEq(kept[len(kept)-1], l, numeric) {
				continue
			}
			kept = append(kept, l)
		}
		lines = kept
	}
	it.LastStatus = 0
	if len(lines) == 0 {
		return "", "", nil
	}
	return strings.Join(lines, "\n") + "\n", "", nil
}

// biUniq 只比较相邻行；-c 输出 GNU 风格的 7 位右对齐计数，-d 只显示重复组。
func (it *Interp) biUniq(args []string, stdin string) (string, string, error) {
	p, err := parseShortFlags(args, "cd", "")
	if err != nil {
		return it.failCmd("uniq", 2, err)
	}
	inputs, err := it.collectTextInputs(p.operands, stdin, "uniq")
	if err != nil {
		return it.failCmd("uniq", 1, err)
	}
	type group struct {
		line  string
		count int
	}
	var groups []group
	for _, in := range inputs {
		for _, line := range splitLines(in.data) {
			if len(groups) > 0 && groups[len(groups)-1].line == line {
				groups[len(groups)-1].count++
			} else {
				groups = append(groups, group{line: line, count: 1})
			}
		}
	}
	var out []string
	for _, g := range groups {
		if hasFlag(p, 'd') && g.count < 2 {
			continue
		}
		line := g.line
		if hasFlag(p, 'c') {
			line = fmt.Sprintf("%7d %s", g.count, line)
		}
		out = append(out, line)
	}
	it.LastStatus = 0
	if len(out) == 0 {
		return "", "", nil
	}
	return strings.Join(out, "\n") + "\n", "", nil
}

// ---------------------------- cut ----------------------------

func parseCutFields(spec string) (map[int]bool, error) {
	set := make(map[int]bool)
	for _, tok := range strings.Split(spec, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		if strings.Contains(tok, "-") {
			halves := strings.SplitN(tok, "-", 2)
			lo, err1 := strconv.Atoi(halves[0])
			hi, err2 := strconv.Atoi(halves[1])
			if err1 != nil || err2 != nil || lo < 1 || hi < lo || hi-lo > 100000 {
				return nil, fmt.Errorf("cut: 无效字段范围 %q", tok)
			}
			for i := lo; i <= hi; i++ {
				set[i] = true
			}
			continue
		}
		n, err := strconv.Atoi(tok)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("cut: 无效字段 %q", tok)
		}
		set[n] = true
	}
	if len(set) == 0 {
		return nil, fmt.Errorf("cut: 缺少字段列表")
	}
	return set, nil
}

// biCut 按分隔符切列，-f 支持 1,3 与 1-3；无分隔符的行原样输出（GNU 行为）。
func (it *Interp) biCut(args []string, stdin string) (string, string, error) {
	p, err := parseShortFlags(args, "d:f:", "df")
	if err != nil {
		return it.failCmd("cut", 1, err)
	}
	if !hasFlag(p, 'f') {
		return it.failCmd("cut", 1, fmt.Errorf("missing -f field list"))
	}
	fields, err := parseCutFields(p.flags['f'])
	if err != nil {
		return it.failCmd("cut", 1, err)
	}
	delim := "\t"
	if hasFlag(p, 'd') {
		if p.flags['d'] == "" {
			return it.failCmd("cut", 1, fmt.Errorf("empty delimiter"))
		}
		delim = p.flags['d'][:1] // 取首字节，与 GNU cut 一致
	}
	inputs, err := it.collectTextInputs(p.operands, stdin, "cut")
	if err != nil {
		return it.failCmd("cut", 1, err)
	}
	var out []string
	for _, in := range inputs {
		for _, line := range splitLines(in.data) {
			if !strings.Contains(line, delim) {
				out = append(out, line)
				continue
			}
			parts := strings.Split(line, delim)
			var chosen []string
			for i, part := range parts {
				if fields[i+1] {
					chosen = append(chosen, part)
				}
			}
			out = append(out, strings.Join(chosen, delim))
		}
	}
	it.LastStatus = 0
	if len(out) == 0 {
		return "", "", nil
	}
	return strings.Join(out, "\n") + "\n", "", nil
}

// ---------------------------- tr ----------------------------

// expandTrSet 展开 SET 中的 a-z/A-Z/0-9 区间与 \n/\t/\r/\\/\- 转义。
func expandTrSet(s string) ([]rune, error) {
	runes, out := []rune(s), []rune(nil)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '\\' && i+1 < len(runes) {
			switch runes[i+1] {
			case 'n':
				out = append(out, '\n')
				i++
				continue
			case 't':
				out = append(out, '\t')
				i++
				continue
			case 'r':
				out = append(out, '\r')
				i++
				continue
			case '\\', '-':
				out = append(out, runes[i+1])
				i++
				continue
			}
		}
		if i+2 < len(runes) && runes[i+1] == '-' {
			lo, hi := r, runes[i+2]
			if lo > hi || hi-lo > 65535 {
				return nil, fmt.Errorf("tr: 无效范围 %c-%c", lo, hi)
			}
			for c := lo; c <= hi; c++ {
				out = append(out, c)
			}
			i += 2
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// biTr 仅支持 SET1 SET2 字符一一替换；完整的 tr 语法（字符类、补集、删除等）明确报错。
func (it *Interp) biTr(args []string, stdin string) (string, string, error) {
	p, err := parseShortFlags(args, "", "")
	if err != nil {
		return it.failCmd("tr", 1, fmt.Errorf("仅支持 SET1 SET2 字符替换，不支持选项: %w", err))
	}
	if len(p.operands) != 2 {
		return it.failCmd("tr", 1, fmt.Errorf("需要两个字符集参数（SET1 SET2）"))
	}
	s1, err := expandTrSet(p.operands[0])
	if err != nil {
		return it.failCmd("tr", 1, err)
	}
	s2, err := expandTrSet(p.operands[1])
	if err != nil {
		return it.failCmd("tr", 1, err)
	}
	if len(s1) == 0 || len(s2) == 0 {
		return it.failCmd("tr", 1, fmt.Errorf("字符集不能为空"))
	}
	// SET2 不足时用其末字符补齐（GNU 行为），多出的字符忽略。
	if len(s2) < len(s1) {
		last := s2[len(s2)-1]
		for len(s2) < len(s1) {
			s2 = append(s2, last)
		}
	}
	mapping := make(map[rune]rune, len(s1))
	for i, r := range s1 {
		if _, exists := mapping[r]; !exists {
			mapping[r] = s2[i]
		}
	}
	var b strings.Builder
	for _, r := range stdin {
		if v, ok := mapping[r]; ok {
			b.WriteRune(v)
		} else {
			b.WriteRune(r)
		}
	}
	it.LastStatus = 0
	return b.String(), "", nil
}

// ---------------------------- less / more / nl ----------------------------

// biPager 是 less/more 的共同实现。非 TTY 环境无法交互翻页，因此明确降级为 cat：
// 直接把文件（或 stdin）的全部内容原样输出，不附加任何提示到 stdout/stderr。
func (it *Interp) biPager(name string, args []string, stdin string) (string, string, error) {
	p, err := parseShortFlags(args, "", "")
	if err != nil {
		return it.failCmd(name, 2, err)
	}
	if len(p.operands) == 0 {
		it.LastStatus = 0
		return stdin, "", nil
	}
	var b strings.Builder
	for _, op := range p.operands {
		data, err := it.readFileArg(op, name)
		if err != nil {
			return it.failCmd(name, 1, err)
		}
		b.WriteString(data)
	}
	it.LastStatus = 0
	return b.String(), "", nil
}

func (it *Interp) biLess(args []string, stdin string) (string, string, error) {
	return it.biPager("less", args, stdin)
}

func (it *Interp) biMore(args []string, stdin string) (string, string, error) {
	return it.biPager("more", args, stdin)
}

// biNl 输出带行号的文本，格式对齐 GNU nl 默认样式：6 位右对齐 + 制表符分隔。
// 与 cat -n 一致，空行同样编号。
func (it *Interp) biNl(args []string, stdin string) (string, string, error) {
	p, err := parseShortFlags(args, "", "")
	if err != nil {
		return it.failCmd("nl", 2, err)
	}
	inputs, err := it.collectTextInputs(p.operands, stdin, "nl")
	if err != nil {
		return it.failCmd("nl", 1, err)
	}
	var b strings.Builder
	n := 0
	for _, in := range inputs {
		for _, line := range splitLines(in.data) {
			n++
			fmt.Fprintf(&b, "%6d\t%s\n", n, line)
		}
	}
	it.LastStatus = 0
	return b.String(), "", nil
}
