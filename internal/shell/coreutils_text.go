package shell

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

// textInput 是 wc/head/tail 的输入：文件（含名字）或标准输入（无名字）。
type textInput struct {
	name string
	data string
}

// collectTextInputs 按操作数收集输入；"-" 与无操作数代表标准输入。
func (it *Interp) collectTextInputs(operands []string, stdin, cmd string) ([]textInput, error) {
	if len(operands) == 0 {
		return []textInput{{name: "", data: stdin}}, nil
	}
	var inputs []textInput
	for _, op := range operands {
		if op == "-" {
			inputs = append(inputs, textInput{name: "", data: stdin})
			continue
		}
		data, err := it.readFileArg(op, cmd)
		if err != nil {
			return nil, err
		}
		inputs = append(inputs, textInput{name: op, data: data})
	}
	return inputs, nil
}

func (it *Interp) biWc(args []string, stdin string) (string, string, error) {
	p, err := parseShortFlags(args, "lwcm", "")
	if err != nil {
		return it.failCmd("wc", 2, err)
	}
	inputs, err := it.collectTextInputs(p.operands, stdin, "wc")
	if err != nil {
		return it.failCmd("wc", 1, err)
	}
	// 有任一模式标志时只输出被选中的列；顺序固定为 行 词 字符 字节（GNU 规则）。
	var cols []byte
	if len(p.flags) == 0 {
		cols = []byte{'l', 'w', 'c'}
	} else {
		for _, c := range []byte{'l', 'w', 'm', 'c'} {
			if hasFlag(p, c) {
				cols = append(cols, c)
			}
		}
	}
	type counts struct{ lines, words, chars, bytes int }
	measure := func(s string) counts {
		return counts{strings.Count(s, "\n"), len(strings.Fields(s)), utf8.RuneCountInString(s), len(s)}
	}
	values := func(c counts) []int {
		out := make([]int, 0, len(cols))
		for _, col := range cols {
			switch col {
			case 'l':
				out = append(out, c.lines)
			case 'w':
				out = append(out, c.words)
			case 'm':
				out = append(out, c.chars)
			case 'c':
				out = append(out, c.bytes)
			}
		}
		return out
	}
	// GNU wc 把所有数字（含 total）按最大位数右对齐。
	width := 1
	all := make([][]int, 0, len(inputs)+1)
	totals := make([]int, len(cols))
	for _, in := range inputs {
		vs := values(measure(in.data))
		all = append(all, vs)
		for i, v := range vs {
			totals[i] += v
			if w := len(strconv.Itoa(v)); w > width {
				width = w
			}
		}
	}
	if len(inputs) > 1 {
		all = append(all, totals)
		for _, v := range totals {
			if w := len(strconv.Itoa(v)); w > width {
				width = w
			}
		}
	}
	var lines []string
	for i, vs := range all {
		fields := make([]string, len(vs))
		for j, v := range vs {
			fields[j] = fmt.Sprintf("%*d", width, v)
		}
		line := strings.Join(fields, " ")
		switch {
		case len(inputs) > 1 && i == len(inputs):
			line += " total"
		case inputs[i].name != "":
			line += " " + inputs[i].name
		}
		lines = append(lines, line)
	}
	it.LastStatus = 0
	return strings.Join(lines, "\n") + "\n", "", nil
}

// isDigits 报告 s 是否全为 ASCII 数字且非空。
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// normalizeHeadTailArgs 把 head -3 / tail -3 / tail +3 归一成 -n <count>。
// 紧跟 -n 的数字是参数值（如 -n -1），不能再归一，否则会被当成新选项。
func normalizeHeadTailArgs(args []string, allowPlus bool) []string {
	var out []string
	for i, a := range args {
		if i > 0 && args[i-1] == "-n" {
			out = append(out, a)
			continue
		}
		if len(a) > 1 && a[0] == '-' && isDigits(a[1:]) {
			out = append(out, "-n", a[1:])
			continue
		}
		if allowPlus && len(a) > 1 && a[0] == '+' && isDigits(a[1:]) {
			out = append(out, "-n", a)
			continue
		}
		out = append(out, a)
	}
	return out
}

// parseLineCount 解析 -n 的参数；plus 表示 tail 的 "+N"（从第 N 行起）。
func parseLineCount(s string) (int, bool, error) {
	plus := strings.HasPrefix(s, "+")
	if plus {
		s = s[1:]
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false, fmt.Errorf("invalid number of lines: %q", s)
	}
	return n, plus, nil
}

// headTailOutput 按 GNU 的多文件头格式拼接各输入选中的行。
func headTailOutput(inputs []textInput, selectFn func(string) string) string {
	var b strings.Builder
	for i, in := range inputs {
		if len(inputs) > 1 {
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString("==> " + in.name + " <==\n")
		}
		b.WriteString(selectFn(in.data))
	}
	return b.String()
}

func (it *Interp) biHead(args []string, stdin string) (string, string, error) {
	p, err := parseShortFlags(normalizeHeadTailArgs(args, false), "n", "n")
	if err != nil {
		return it.failCmd("head", 2, err)
	}
	n := 10
	if hasFlag(p, 'n') {
		parsed, plus, err := parseLineCount(p.flags['n'])
		if err != nil || plus {
			return it.failCmd("head", 2, fmt.Errorf("invalid number of lines: %q", p.flags['n']))
		}
		n = parsed
	}
	inputs, err := it.collectTextInputs(p.operands, stdin, "head")
	if err != nil {
		return it.failCmd("head", 1, err)
	}
	selectFn := func(data string) string {
		lines := splitLinesKeepEnd(data)
		switch {
		case n == 0:
			return ""
		case n < 0:
			drop := -n
			if drop >= len(lines) {
				return ""
			}
			lines = lines[:len(lines)-drop]
		case len(lines) > n:
			lines = lines[:n]
		}
		return strings.Join(lines, "")
	}
	it.LastStatus = 0
	return headTailOutput(inputs, selectFn), "", nil
}

func (it *Interp) biTail(args []string, stdin string) (string, string, error) {
	p, err := parseShortFlags(normalizeHeadTailArgs(args, true), "n", "n")
	if err != nil {
		return it.failCmd("tail", 2, err)
	}
	n, plus := 10, false
	if hasFlag(p, 'n') {
		n, plus, err = parseLineCount(p.flags['n'])
		if err != nil {
			return it.failCmd("tail", 2, err)
		}
	}
	inputs, err := it.collectTextInputs(p.operands, stdin, "tail")
	if err != nil {
		return it.failCmd("tail", 1, err)
	}
	selectFn := func(data string) string {
		lines := splitLinesKeepEnd(data)
		switch {
		case plus:
			if n < 1 || n > len(lines) {
				return ""
			}
			lines = lines[n-1:]
		case n == 0:
			return ""
		case n < 0:
			skip := -n
			if skip >= len(lines) {
				return ""
			}
			lines = lines[skip:]
		case len(lines) > n:
			lines = lines[len(lines)-n:]
		}
		return strings.Join(lines, "")
	}
	it.LastStatus = 0
	return headTailOutput(inputs, selectFn), "", nil
}

func (it *Interp) biTee(args []string, stdin string) (string, string, error) {
	p, err := parseShortFlags(args, "a", "")
	if err != nil {
		return it.failCmd("tee", 2, err)
	}
	if len(p.operands) == 0 {
		it.LastStatus = 0
		return stdin, "", nil
	}
	flag := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if hasFlag(p, 'a') {
		flag = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	}
	var firstErr error
	for _, op := range p.operands {
		abs, err := it.resolvePath(op)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		f, err := os.OpenFile(abs, flag, 0o644)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", op, err)
			}
			continue
		}
		_, err = f.WriteString(stdin)
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		if err != nil && firstErr == nil {
			firstErr = fmt.Errorf("%s: %w", op, err)
		}
	}
	it.LastStatus = 0
	if firstErr != nil {
		it.LastStatus = 1
		return stdin, fmt.Sprintf("shell: tee: %v\n", firstErr), nil
	}
	return stdin, "", nil
}
