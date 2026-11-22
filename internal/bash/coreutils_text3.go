package bash

import (
	"fmt"
	"strconv"
	"strings"
)

// 本文件补齐一批纯 Go 文本处理内建命令：tac/diff/comm/paste/rev/fold/expand。
// 文件读取统一经 collectTextInputs/readFileArg（内部走 resolvePath），因此受工作区
// 边界约束；不调用任何外部程序，也不依赖 PATH。三参数签名与 shell 内建约定一致：
// 返回 (stdout, stderr, Go error)，退出码写入 it.LastStatus。

// ---------------------------- tac ----------------------------

// biTac 按行倒序输出。GNU tac 把行尾换行视作行的一部分，因此末行若无换行，
// 反转后会成为首行且不带换行；多文件时逐个文件分别反转再拼接。
func (it *Interp) biTac(args []string, stdin string) (string, string, error) {
	p, err := parseShortFlags(args, "", "")
	if err != nil {
		return it.failCmd("tac", 1, err)
	}
	inputs, err := it.collectTextInputs(p.operands, stdin, "tac")
	if err != nil {
		return it.failCmd("tac", 1, err)
	}
	var b strings.Builder
	for _, in := range inputs {
		lines := splitLinesKeepEnd(in.data)
		for i := len(lines) - 1; i >= 0; i-- {
			b.WriteString(lines[i])
		}
	}
	it.LastStatus = 0
	return b.String(), "", nil
}

// ---------------------------- diff ----------------------------

// text3DiffOp 是差异脚本中的一行：' ' 表示相同，'-' 仅见于文件1，'+' 仅见于文件2。
type text3DiffOp struct {
	kind byte
	line string
	old  int // 该行在文件1中的行号（'+' 行为插入点）
	new  int // 该行在文件2中的行号（'-' 行为插入点）
}

// text3Annotate 给差异脚本补上行号；行号从 1 起，0 仅用作"尚未取值"的哨兵。
func text3Annotate(raw []text3DiffOp) []text3DiffOp {
	o, n := 1, 1
	for i := range raw {
		raw[i].old, raw[i].new = o, n
		switch raw[i].kind {
		case ' ':
			o++
			n++
		case '-':
			o++
		default:
			n++
		}
	}
	return raw
}

// text3DiffOps 用 LCS 生成最小编辑脚本。超大输入退化为逐行替换，避免 O(n*m) 内存爆炸。
func text3DiffOps(a, b []string) []text3DiffOp {
	var raw []text3DiffOp
	n, m := len(a), len(b)
	if n > 1500 || m > 1500 || n*m > 1_500_000 {
		for i := 0; i < n || i < m; i++ {
			switch {
			case i < n && i < m && a[i] == b[i]:
				raw = append(raw, text3DiffOp{kind: ' ', line: a[i]})
			case i < n && i < m:
				raw = append(raw, text3DiffOp{kind: '-', line: a[i]}, text3DiffOp{kind: '+', line: b[i]})
			case i < n:
				raw = append(raw, text3DiffOp{kind: '-', line: a[i]})
			default:
				raw = append(raw, text3DiffOp{kind: '+', line: b[i]})
			}
		}
		return text3Annotate(raw)
	}
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			raw = append(raw, text3DiffOp{kind: ' ', line: a[i]})
			i, j = i+1, j+1
		case dp[i+1][j] >= dp[i][j+1]:
			raw = append(raw, text3DiffOp{kind: '-', line: a[i]})
			i++
		default:
			raw = append(raw, text3DiffOp{kind: '+', line: b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		raw = append(raw, text3DiffOp{kind: '-', line: a[i]})
	}
	for ; j < m; j++ {
		raw = append(raw, text3DiffOp{kind: '+', line: b[j]})
	}
	return text3Annotate(raw)
}

// text3NormalDiff 输出普通格式的差异块：先列 '-' 行（< 前缀），再列 '+' 行（> 前缀）。
func text3NormalDiff(ops []text3DiffOp) string {
	var b strings.Builder
	for i := 0; i < len(ops); {
		if ops[i].kind == ' ' {
			i++
			continue
		}
		start := i
		for i < len(ops) && ops[i].kind != ' ' {
			i++
		}
		for _, op := range ops[start:i] {
			if op.kind == '-' {
				b.WriteString("< " + op.line + "\n")
			}
		}
		for _, op := range ops[start:i] {
			if op.kind == '+' {
				b.WriteString("> " + op.line + "\n")
			}
		}
	}
	return b.String()
}

// text3UnifiedDiff 输出 unified 格式：文件头 + 若干 hunk，每处改动保留 3 行上下文。
func text3UnifiedDiff(name1, name2 string, ops []text3DiffOp) string {
	changes := make([]int, 0)
	for i, op := range ops {
		if op.kind != ' ' {
			changes = append(changes, i)
		}
	}
	if len(changes) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("--- " + name1 + "\n")
	b.WriteString("+++ " + name2 + "\n")
	const ctx = 3
	for g := 0; g < len(changes); {
		last := changes[g]
		h := g + 1
		for h < len(changes) && changes[h]-changes[h-1]-1 <= 2*ctx {
			last = changes[h]
			h++
		}
		lo, hi := changes[g]-ctx, last+ctx
		if lo < 0 {
			lo = 0
		}
		if hi >= len(ops) {
			hi = len(ops) - 1
		}
		oldStart, newStart, oldCount, newCount := 0, 0, 0, 0
		for k := lo; k <= hi; k++ {
			switch ops[k].kind {
			case ' ':
				if oldStart == 0 {
					oldStart, newStart = ops[k].old, ops[k].new
				}
				oldCount++
				newCount++
			case '-':
				if oldStart == 0 {
					oldStart = ops[k].old
				}
				oldCount++
			default:
				if newStart == 0 {
					newStart = ops[k].new
				}
				newCount++
			}
		}
		if oldCount == 0 {
			oldStart = ops[lo].old - 1
			if oldStart < 0 {
				oldStart = 0
			}
		}
		if newCount == 0 {
			newStart = ops[lo].new - 1
			if newStart < 0 {
				newStart = 0
			}
		}
		oldRange, newRange := strconv.Itoa(oldStart), strconv.Itoa(newStart)
		if oldCount != 1 {
			oldRange += "," + strconv.Itoa(oldCount)
		}
		if newCount != 1 {
			newRange += "," + strconv.Itoa(newCount)
		}
		fmt.Fprintf(&b, "@@ -%s +%s @@\n", oldRange, newRange)
		for k := lo; k <= hi; k++ {
			b.WriteByte(ops[k].kind)
			b.WriteString(ops[k].line)
			b.WriteByte('\n')
		}
		g = h
	}
	return b.String()
}

// biDiff 比较两个文件；相同退出 0，不同退出 1，参数或读取错误退出 2。
func (it *Interp) biDiff(args []string, stdin string) (string, string, error) {
	p, err := parseShortFlags(args, "u", "")
	if err != nil {
		return it.failCmd("diff", 2, err)
	}
	if len(p.operands) != 2 {
		return it.failCmd("diff", 2, fmt.Errorf("需要两个文件参数"))
	}
	name1, name2 := p.operands[0], p.operands[1]
	d1, err := it.readFileArg(name1, "diff")
	if err != nil {
		return it.failCmd("diff", 2, err)
	}
	d2, err := it.readFileArg(name2, "diff")
	if err != nil {
		return it.failCmd("diff", 2, err)
	}
	ops := text3DiffOps(splitLines(d1), splitLines(d2))
	changed := false
	for _, op := range ops {
		if op.kind != ' ' {
			changed = true
			break
		}
	}
	if !changed {
		it.LastStatus = 0
		return "", "", nil
	}
	if hasFlag(p, 'u') {
		it.LastStatus = 1
		return text3UnifiedDiff(name1, name2, ops), "", nil
	}
	it.LastStatus = 1
	return text3NormalDiff(ops), "", nil
}

// ---------------------------- comm ----------------------------

// biComm 比较两个已排序文件，输出三列：仅文件1 / 仅文件2 / 两者共有。
// -1/-2/-3 抑制相应列；被抑制列的制表符一同省略，仅保留后续可见列的前导 tab。
func (it *Interp) biComm(args []string, stdin string) (string, string, error) {
	p, err := parseShortFlags(args, "123", "")
	if err != nil {
		return it.failCmd("comm", 2, err)
	}
	if len(p.operands) != 2 {
		return it.failCmd("comm", 2, fmt.Errorf("需要两个已排序的文件参数"))
	}
	d1, err := it.readFileArg(p.operands[0], "comm")
	if err != nil {
		return it.failCmd("comm", 1, err)
	}
	d2, err := it.readFileArg(p.operands[1], "comm")
	if err != nil {
		return it.failCmd("comm", 1, err)
	}
	present := make([]int, 0, 3)
	for c := 1; c <= 3; c++ {
		if !hasFlag(p, byte('0'+c)) {
			present = append(present, c)
		}
	}
	prefix := func(col int) string {
		n := 0
		for _, pc := range present {
			if pc == col {
				break
			}
			n++
		}
		return strings.Repeat("\t", n)
	}
	a, b := splitLines(d1), splitLines(d2)
	i, j := 0, 0
	var out strings.Builder
	emit := func(col int, line string) {
		if !hasFlag(p, byte('0'+col)) {
			out.WriteString(prefix(col))
			out.WriteString(line)
			out.WriteByte('\n')
		}
	}
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			emit(3, a[i])
			i, j = i+1, j+1
		case a[i] < b[j]:
			emit(1, a[i])
			i++
		default:
			emit(2, b[j])
			j++
		}
	}
	for ; i < len(a); i++ {
		emit(1, a[i])
	}
	for ; j < len(b); j++ {
		emit(2, b[j])
	}
	it.LastStatus = 0
	return out.String(), "", nil
}

// ---------------------------- paste ----------------------------

// text3PasteDelims 解析 -d：支持 \t/\n/\\ 转义，多字符时按位置循环取用（GNU 行为）。
func text3PasteDelims(s string) ([]rune, error) {
	var ds []rune
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case 't':
				ds = append(ds, '\t')
			case 'n':
				ds = append(ds, '\n')
			case '\\':
				ds = append(ds, '\\')
			default:
				ds = append(ds, '\\', rune(s[i+1]))
			}
			i++
			continue
		}
		ds = append(ds, rune(s[i]))
	}
	if len(ds) == 0 {
		return nil, fmt.Errorf("empty delimiter")
	}
	return ds, nil
}

// biPaste 按行横向拼接各输入，短文件用空字段补齐，输出行数取各输入的最大值。
func (it *Interp) biPaste(args []string, stdin string) (string, string, error) {
	p, err := parseShortFlags(args, "d", "d")
	if err != nil {
		return it.failCmd("paste", 2, err)
	}
	delims := []rune{'\t'}
	if hasFlag(p, 'd') {
		delims, err = text3PasteDelims(p.flags['d'])
		if err != nil {
			return it.failCmd("paste", 2, err)
		}
	}
	inputs, err := it.collectTextInputs(p.operands, stdin, "paste")
	if err != nil {
		return it.failCmd("paste", 1, err)
	}
	lines := make([][]string, len(inputs))
	rows := 0
	for k, in := range inputs {
		lines[k] = splitLines(in.data)
		if len(lines[k]) > rows {
			rows = len(lines[k])
		}
	}
	var out strings.Builder
	for row := 0; row < rows; row++ {
		for col := 0; col < len(inputs); col++ {
			if col > 0 {
				out.WriteRune(delims[(col-1)%len(delims)])
			}
			if row < len(lines[col]) {
				out.WriteString(lines[col][row])
			}
		}
		out.WriteByte('\n')
	}
	it.LastStatus = 0
	return out.String(), "", nil
}

// ---------------------------- rev ----------------------------

// biRev 逐行按 rune 反转，UTF-8 安全；行尾换行留在原位，多文件分别处理。
func (it *Interp) biRev(args []string, stdin string) (string, string, error) {
	p, err := parseShortFlags(args, "", "")
	if err != nil {
		return it.failCmd("rev", 1, err)
	}
	inputs, err := it.collectTextInputs(p.operands, stdin, "rev")
	if err != nil {
		return it.failCmd("rev", 1, err)
	}
	var out strings.Builder
	for _, in := range inputs {
		for _, chunk := range splitLinesKeepEnd(in.data) {
			body, nl := chunk, ""
			if strings.HasSuffix(chunk, "\n") {
				body, nl = chunk[:len(chunk)-1], "\n"
			}
			r := []rune(body)
			for l, h := 0, len(r)-1; l < h; l, h = l+1, h-1 {
				r[l], r[h] = r[h], r[l]
			}
			out.WriteString(string(r))
			out.WriteString(nl)
		}
	}
	it.LastStatus = 0
	return out.String(), "", nil
}

// ---------------------------- fold / expand ----------------------------

// text3NormalizeNumFlag 把 GNU 的 -N 归一成 -w N / -t N，便于统一解析。
func text3NormalizeNumFlag(args []string, flag byte) []string {
	out := make([]string, 0, len(args))
	needle := "-" + string(flag)
	for i, a := range args {
		if i > 0 && args[i-1] == needle {
			out = append(out, a)
			continue
		}
		if len(a) > 1 && a[0] == '-' && isDigits(a[1:]) {
			out = append(out, needle, a[1:])
			continue
		}
		out = append(out, a)
	}
	return out
}

// biFold 按显示宽度折行，默认 80；按 rune 计数，末行有无换行与原输入保持一致。
func (it *Interp) biFold(args []string, stdin string) (string, string, error) {
	p, err := parseShortFlags(text3NormalizeNumFlag(args, 'w'), "w", "w")
	if err != nil {
		return it.failCmd("fold", 2, err)
	}
	width := 80
	if hasFlag(p, 'w') {
		width, err = strconv.Atoi(p.flags['w'])
		if err != nil || width < 1 {
			return it.failCmd("fold", 2, fmt.Errorf("invalid width %q", p.flags['w']))
		}
	}
	inputs, err := it.collectTextInputs(p.operands, stdin, "fold")
	if err != nil {
		return it.failCmd("fold", 1, err)
	}
	var out strings.Builder
	for _, in := range inputs {
		for _, chunk := range splitLinesKeepEnd(in.data) {
			body, nl := chunk, ""
			if strings.HasSuffix(chunk, "\n") {
				body, nl = chunk[:len(chunk)-1], "\n"
			}
			r := []rune(body)
			for len(r) > width {
				out.WriteString(string(r[:width]))
				out.WriteByte('\n')
				r = r[width:]
			}
			out.WriteString(string(r))
			out.WriteString(nl)
		}
	}
	it.LastStatus = 0
	return out.String(), "", nil
}

// biExpand 把 tab 转成空格并对齐制表位（默认 8），换行与退格重置/回退列计数。
func (it *Interp) biExpand(args []string, stdin string) (string, string, error) {
	p, err := parseShortFlags(text3NormalizeNumFlag(args, 't'), "t", "t")
	if err != nil {
		return it.failCmd("expand", 2, err)
	}
	tab := 8
	if hasFlag(p, 't') {
		tab, err = strconv.Atoi(p.flags['t'])
		if err != nil || tab < 1 {
			return it.failCmd("expand", 2, fmt.Errorf("invalid tab stop %q", p.flags['t']))
		}
	}
	inputs, err := it.collectTextInputs(p.operands, stdin, "expand")
	if err != nil {
		return it.failCmd("expand", 1, err)
	}
	var out strings.Builder
	for _, in := range inputs {
		col := 0
		for _, r := range in.data {
			switch r {
			case '\t':
				spaces := tab - col%tab
				out.WriteString(strings.Repeat(" ", spaces))
				col += spaces
			case '\n', '\r':
				out.WriteRune(r)
				col = 0
			case '\b':
				out.WriteRune(r)
				if col > 0 {
					col--
				}
			default:
				out.WriteRune(r)
				col++
			}
		}
	}
	it.LastStatus = 0
	return out.String(), "", nil
}
