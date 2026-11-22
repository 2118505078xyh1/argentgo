package bash

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// biExpr 求值整数表达式：四则运算 + - * / %、比较 = != < > <= >=、逻辑 & |。
// 参数按空格拼成一个表达式，因此 `expr 1 + 2` 与 `expr "1 + 2"` 都能工作。
// shell 里的 * 是 glob，但传到这里时参数已展开为字面量，可直接当乘号。
func (it *Interp) biExpr(args []string, stdin string) (string, string, error) {
	_ = stdin
	src := strings.Join(args, " ")
	if strings.TrimSpace(src) == "" {
		return it.failCmd("expr", 1, fmt.Errorf("missing expression"))
	}
	toks, err := tokenizeExpr(src)
	if err != nil {
		return it.failCmd("expr", 1, err)
	}
	p := &exprParser{toks: toks}
	v, err := p.parseOr()
	if err != nil {
		return it.failCmd("expr", 1, err)
	}
	if p.pos != len(p.toks) {
		return it.failCmd("expr", 1, fmt.Errorf("expr: 多余的词法单元 %q", p.toks[p.pos]))
	}
	it.LastStatus = 0
	if v.isNum {
		return strconv.FormatInt(v.num, 10) + "\n", "", nil
	}
	return v.text + "\n", "", nil
}

// tokenizeExpr 把表达式切成词法单元；多字符比较符按最长匹配，引号内内容原样保留。
func tokenizeExpr(s string) ([]string, error) {
	var toks []string
	isOp := func(c byte) bool { return strings.IndexByte("+-*/%=<>&|()", c) >= 0 }
	isSpace := func(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }
	for i := 0; i < len(s); {
		c := s[i]
		if isSpace(c) {
			i++
			continue
		}
		if c == '\'' || c == '"' {
			j := i + 1
			for j < len(s) && s[j] != c {
				j++
			}
			if j >= len(s) {
				return nil, fmt.Errorf("expr: 引号未闭合")
			}
			toks = append(toks, s[i+1:j])
			i = j + 1
			continue
		}
		if i+1 < len(s) {
			two := s[i : i+2]
			if two == "!=" || two == "<=" || two == ">=" {
				toks = append(toks, two)
				i += 2
				continue
			}
		}
		if isOp(c) {
			toks = append(toks, string(c))
			i++
			continue
		}
		j := i
		for j < len(s) && !isSpace(s[j]) && !isOp(s[j]) && s[j] != '\'' && s[j] != '"' {
			j++
		}
		toks = append(toks, s[i:j])
		i = j
	}
	return toks, nil
}

// exprValue 同时承载整数与字符串两种 expr 值；比较失败时回退到字符串比较。
type exprValue struct {
	text  string
	num   int64
	isNum bool
}

func exprNum(n int64) exprValue {
	return exprValue{text: strconv.FormatInt(n, 10), num: n, isNum: true}
}

func (v exprValue) intVal() (int64, error) {
	if v.isNum {
		return v.num, nil
	}
	n, err := strconv.ParseInt(v.text, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("expr: 非整数参数 %q", v.text)
	}
	return n, nil
}

func (v exprValue) truthy() bool {
	if v.isNum {
		return v.num != 0
	}
	return v.text != ""
}

func exprBoolInt(ok bool) int64 {
	if ok {
		return 1
	}
	return 0
}

// exprCompare 先按整数比较，任一操作数不是整数时退化为字典序字符串比较。
func exprCompare(a, b exprValue) int {
	an, ae := a.intVal()
	bn, be := b.intVal()
	if ae == nil && be == nil {
		switch {
		case an < bn:
			return -1
		case an > bn:
			return 1
		default:
			return 0
		}
	}
	return strings.Compare(a.text, b.text)
}

// exprParser 是递归下降解析器，优先级由低到高：| & 比较 + - * / %。
type exprParser struct {
	toks []string
	pos  int
}

func (p *exprParser) peek() string {
	if p.pos < len(p.toks) {
		return p.toks[p.pos]
	}
	return ""
}

func (p *exprParser) parseOr() (exprValue, error) {
	v, err := p.parseAnd()
	if err != nil {
		return exprValue{}, err
	}
	for p.peek() == "|" {
		p.pos++
		r, e := p.parseAnd()
		if e != nil {
			return exprValue{}, e
		}
		if !v.truthy() { // GNU expr 的 | 在左值为 0 或空串时返回右值
			v = r
		}
	}
	return v, nil
}

func (p *exprParser) parseAnd() (exprValue, error) {
	v, err := p.parseCompare()
	if err != nil {
		return exprValue{}, err
	}
	for p.peek() == "&" {
		p.pos++
		r, e := p.parseCompare()
		if e != nil {
			return exprValue{}, e
		}
		if !v.truthy() || !r.truthy() {
			v = exprNum(0)
		}
	}
	return v, nil
}

func (p *exprParser) parseCompare() (exprValue, error) {
	a, err := p.parseSum()
	if err != nil {
		return exprValue{}, err
	}
	op := p.peek()
	switch op {
	case "=", "!=", "<", ">", "<=", ">=":
		p.pos++
		b, e := p.parseSum()
		if e != nil {
			return exprValue{}, e
		}
		c := exprCompare(a, b)
		var res int64
		switch op {
		case "=":
			res = exprBoolInt(c == 0)
		case "!=":
			res = exprBoolInt(c != 0)
		case "<":
			res = exprBoolInt(c < 0)
		case ">":
			res = exprBoolInt(c > 0)
		case "<=":
			res = exprBoolInt(c <= 0)
		case ">=":
			res = exprBoolInt(c >= 0)
		}
		return exprNum(res), nil
	}
	return a, nil
}

// exprArith 把两个操作数转成整数后做单一算术运算，集中处理非整数与除零。
func exprArith(a, b exprValue, op byte) (exprValue, error) {
	x, err := a.intVal()
	if err != nil {
		return exprValue{}, err
	}
	y, err := b.intVal()
	if err != nil {
		return exprValue{}, err
	}
	switch op {
	case '+':
		return exprNum(x + y), nil
	case '-':
		return exprNum(x - y), nil
	case '*':
		return exprNum(x * y), nil
	case '/':
		if y == 0 {
			return exprValue{}, fmt.Errorf("expr: 除数不能为零")
		}
		return exprNum(x / y), nil
	case '%':
		if y == 0 {
			return exprValue{}, fmt.Errorf("expr: 除数不能为零")
		}
		return exprNum(x % y), nil
	}
	return exprValue{}, fmt.Errorf("expr: 未知运算符 %q", op)
}

func (p *exprParser) parseSum() (exprValue, error) {
	v, err := p.parseProduct()
	if err != nil {
		return exprValue{}, err
	}
	for p.peek() == "+" || p.peek() == "-" {
		op := p.peek()[0]
		p.pos++
		r, e := p.parseProduct()
		if e != nil {
			return exprValue{}, e
		}
		v, e = exprArith(v, r, op)
		if e != nil {
			return exprValue{}, e
		}
	}
	return v, nil
}

func (p *exprParser) parseProduct() (exprValue, error) {
	v, err := p.parsePrimary()
	if err != nil {
		return exprValue{}, err
	}
	for op := p.peek(); op == "*" || op == "/" || op == "%"; op = p.peek() {
		p.pos++
		r, e := p.parsePrimary()
		if e != nil {
			return exprValue{}, e
		}
		v, e = exprArith(v, r, op[0])
		if e != nil {
			return exprValue{}, e
		}
	}
	return v, nil
}

func (p *exprParser) parsePrimary() (exprValue, error) {
	tok := p.peek()
	if tok == "" {
		return exprValue{}, fmt.Errorf("expr: 表达式意外结束")
	}
	if tok == "-" { // 支持 1 + -2 这类一元负号
		p.pos++
		v, err := p.parsePrimary()
		if err != nil {
			return exprValue{}, err
		}
		n, err := v.intVal()
		if err != nil {
			return exprValue{}, err
		}
		return exprNum(-n), nil
	}
	if tok == "(" {
		p.pos++
		v, err := p.parseOr()
		if err != nil {
			return exprValue{}, err
		}
		if p.peek() != ")" {
			return exprValue{}, fmt.Errorf("expr: 缺少右括号")
		}
		p.pos++
		return v, nil
	}
	if strings.IndexByte("+-*/%=<>&|()", tok[0]) >= 0 {
		return exprValue{}, fmt.Errorf("expr: 意外的词法单元 %q", tok)
	}
	p.pos++
	n, err := strconv.ParseInt(tok, 10, 64)
	if err != nil {
		return exprValue{text: tok}, nil
	}
	return exprNum(n), nil
}

// biBc 是浮点计算器：有参数时求值拼接后的表达式，无参数时逐行读取 stdin 求值。
// 仅支持数值四则运算、取余、括号与小数，不支持变量/函数（如 sqrt、a=1）。
func (it *Interp) biBc(args []string, stdin string) (string, string, error) {
	var lines []string
	if len(args) > 0 {
		lines = []string{strings.Join(args, " ")}
	} else {
		lines = splitLines(stdin)
	}
	var out strings.Builder
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		for _, seg := range strings.Split(line, ";") {
			seg = strings.TrimSpace(seg)
			if seg == "" {
				continue
			}
			p := &bcParser{src: seg}
			v, err := p.parseExpr()
			if err != nil {
				return it.failCmd("bc", 1, err)
			}
			p.skipSpace()
			if p.pos != len(p.src) {
				return it.failCmd("bc", 1, fmt.Errorf("bc: 无法解析的多余内容 %q", p.src[p.pos:]))
			}
			out.WriteString(formatBcNum(v))
			out.WriteByte('\n')
		}
	}
	it.LastStatus = 0
	return out.String(), "", nil
}

// bcParser 对单个表达式做递归下降解析：expr → term → unary → primary。
type bcParser struct {
	src string
	pos int
}

func (p *bcParser) skipSpace() {
	for p.pos < len(p.src) && (p.src[p.pos] == ' ' || p.src[p.pos] == '\t') {
		p.pos++
	}
}

func (p *bcParser) parseExpr() (float64, error) {
	v, err := p.parseTerm()
	if err != nil {
		return 0, err
	}
	for {
		p.skipSpace()
		if p.pos >= len(p.src) || (p.src[p.pos] != '+' && p.src[p.pos] != '-') {
			break
		}
		op := p.src[p.pos]
		p.pos++
		r, e := p.parseTerm()
		if e != nil {
			return 0, e
		}
		if op == '+' {
			v += r
		} else {
			v -= r
		}
	}
	return v, nil
}

func (p *bcParser) parseTerm() (float64, error) {
	v, err := p.parseUnary()
	if err != nil {
		return 0, err
	}
	for {
		p.skipSpace()
		if p.pos >= len(p.src) {
			break
		}
		op := p.src[p.pos]
		if op != '*' && op != '/' && op != '%' {
			break
		}
		p.pos++
		r, e := p.parseUnary()
		if e != nil {
			return 0, e
		}
		if (op == '/' || op == '%') && r == 0 {
			return 0, fmt.Errorf("bc: 除数不能为零")
		}
		switch op {
		case '*':
			v *= r
		case '/':
			v /= r
		case '%':
			v = math.Mod(v, r)
		}
	}
	return v, nil
}

func (p *bcParser) parseUnary() (float64, error) {
	p.skipSpace()
	if p.pos < len(p.src) && (p.src[p.pos] == '+' || p.src[p.pos] == '-') {
		op := p.src[p.pos]
		p.pos++
		v, err := p.parseUnary()
		if err != nil {
			return 0, err
		}
		if op == '-' {
			return -v, nil
		}
		return v, nil
	}
	return p.parsePrimary()
}

func (p *bcParser) parsePrimary() (float64, error) {
	p.skipSpace()
	if p.pos >= len(p.src) {
		return 0, fmt.Errorf("bc: 表达式不完整")
	}
	c := p.src[p.pos]
	if c == '(' {
		p.pos++
		v, err := p.parseExpr()
		if err != nil {
			return 0, err
		}
		p.skipSpace()
		if p.pos >= len(p.src) || p.src[p.pos] != ')' {
			return 0, fmt.Errorf("bc: 缺少右括号")
		}
		p.pos++
		return v, nil
	}
	if (c >= '0' && c <= '9') || c == '.' {
		start := p.pos
		for p.pos < len(p.src) {
			d := p.src[p.pos]
			if (d >= '0' && d <= '9') || d == '.' {
				p.pos++
				continue
			}
			break
		}
		v, err := strconv.ParseFloat(p.src[start:p.pos], 64)
		if err != nil {
			return 0, fmt.Errorf("bc: 无效数字 %q", p.src[start:p.pos])
		}
		return v, nil
	}
	if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' {
		return 0, fmt.Errorf("bc: 不支持变量/函数等高级特性，仅支持数值四则运算与括号（如 bc '1+2*3'）")
	}
	return 0, fmt.Errorf("bc: 无法识别的字符 %q", string(c))
}

// formatBcNum 去掉浮点结果末尾多余的 0，并把 -0 归一成 0。
func formatBcNum(v float64) string {
	s := strconv.FormatFloat(v, 'f', -1, 64)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
		s = strings.TrimSuffix(s, ".")
	}
	if s == "-0" {
		s = "0"
	}
	return s
}

// biSleep 睡眠指定时长（1、0.5、1s、100ms），上限 60 秒且可被 ctx 取消。
func (it *Interp) biSleep(args []string, stdin string) (string, string, error) {
	_ = stdin
	if len(args) == 0 {
		return it.failCmd("sleep", 1, fmt.Errorf("sleep: 缺少时长参数"))
	}
	d, err := parseSleepDuration(strings.Join(args, ""))
	if err != nil {
		return it.failCmd("sleep", 1, err)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-it.ctx().Done():
		return it.failCmd("sleep", 130, fmt.Errorf("sleep: 已因取消而中断"))
	case <-t.C:
		it.LastStatus = 0
		return "", "", nil
	}
}

// parseSleepDuration 解析「数字 + 可选单位」；m/h 超过 60 秒时直接报错，避免挂死。
func parseSleepDuration(spec string) (time.Duration, error) {
	s := strings.ReplaceAll(strings.TrimSpace(spec), " ", "")
	i := 0
	for i < len(s) && ((s[i] >= '0' && s[i] <= '9') || s[i] == '.') {
		i++
	}
	if i == 0 {
		return 0, fmt.Errorf("sleep: 无效时长 %q", spec)
	}
	n, err := strconv.ParseFloat(s[:i], 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("sleep: 无效时长 %q", spec)
	}
	var unit time.Duration
	switch s[i:] {
	case "", "s":
		unit = time.Second
	case "ms":
		unit = time.Millisecond
	case "us", "µs":
		unit = time.Microsecond
	case "ns":
		unit = time.Nanosecond
	case "m":
		unit = time.Minute
	case "h":
		unit = time.Hour
	default:
		return 0, fmt.Errorf("sleep: 不支持的时间单位 %q", s[i:])
	}
	d := time.Duration(n * float64(unit))
	if d < 0 {
		return 0, fmt.Errorf("sleep: 时长溢出")
	}
	if d > 60*time.Second {
		return 0, fmt.Errorf("sleep: 时长 %s 超过 60 秒上限", s)
	}
	return d, nil
}

// biBzip2 是诚实的桩：Go 标准库没有 bzip2 编解码器，绝不假装成功。
func (it *Interp) biBzip2(args []string, stdin string) (string, string, error) {
	if _, err := parseShortFlags(args, "cdk", ""); err != nil {
		return it.failCmd("bzip2", 2, err)
	}
	return it.failCmd("bzip2", 1, fmt.Errorf("bzip2 格式需要外部库，内建 shell 不支持 bzip2/bunzip2，请使用 gzip 或 zip"))
}

// biXz 是诚实的桩：Go 标准库没有 xz/lzma 编解码器。
func (it *Interp) biXz(args []string, stdin string) (string, string, error) {
	if _, err := parseShortFlags(args, "cdk", ""); err != nil {
		return it.failCmd("xz", 2, err)
	}
	return it.failCmd("xz", 1, fmt.Errorf("xz 格式需要外部库，内建 shell 不支持，请使用 gzip"))
}

// biUnxz 等价于 xz -d，同样明确拒绝，不引入外部依赖。
func (it *Interp) biUnxz(args []string, stdin string) (string, string, error) {
	if _, err := parseShortFlags(args, "cdk", ""); err != nil {
		return it.failCmd("unxz", 2, err)
	}
	return it.failCmd("unxz", 1, fmt.Errorf("xz 格式需要外部库，内建 shell 不支持，请使用 gzip"))
}
