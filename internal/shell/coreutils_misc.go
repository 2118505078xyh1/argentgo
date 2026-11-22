package shell

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// biPrintf 实现 GNU printf 常用的 %s/%d/%q、宽度/精度与反斜杠转义。
// 格式会被复用直到参数耗尽；无参数时按空值处理一次。
func (it *Interp) biPrintf(args []string) (string, string, error) {
	if len(args) == 0 {
		return it.failCmd("printf", 1, fmt.Errorf("missing format argument"))
	}
	format, rest := args[0], args[1:]
	var b strings.Builder
	for {
		text, consumed, err := printfOnce(format, rest)
		if err != nil {
			return it.failCmd("printf", 1, err)
		}
		b.WriteString(text)
		if consumed == 0 || len(rest) <= consumed {
			break
		}
		rest = rest[consumed:]
	}
	it.LastStatus = 0
	return b.String(), "", nil
}

func printfOnce(format string, args []string) (string, int, error) {
	var b strings.Builder
	i, ai := 0, 0
	nextArg := func() string {
		if ai < len(args) {
			s := args[ai]
			ai++
			return s
		}
		return ""
	}
	for i < len(format) {
		c := format[i]
		if c == '%' {
			i++
			if i >= len(format) {
				b.WriteByte('%')
				break
			}
			if format[i] == '%' {
				b.WriteByte('%')
				i++
				continue
			}
			left, zero := false, false
			for i < len(format) {
				switch format[i] {
				case '-':
					left = true
					i++
				case '0':
					zero = true
					i++
				case '+', ' ', '#':
					i++
				default:
					goto flagsDone
				}
			}
		flagsDone:
			width := 0
			for i < len(format) && asciiDigit(format[i]) {
				width = width*10 + int(format[i]-'0')
				i++
			}
			prec := -1
			if i < len(format) && format[i] == '.' {
				i++
				prec = 0
				for i < len(format) && asciiDigit(format[i]) {
					prec = prec*10 + int(format[i]-'0')
					i++
				}
			}
			if i >= len(format) {
				return "", ai, fmt.Errorf("unterminated format")
			}
			verb := format[i]
			i++
			switch verb {
			case 's':
				s := nextArg()
				if prec >= 0 && len(s) > prec {
					s = s[:prec]
				}
				b.WriteString(padField(s, width, left, ' '))
			case 'd', 'i':
				n, err := strconv.ParseInt(nextArg(), 0, 64)
				if err != nil {
					return "", ai, fmt.Errorf("invalid number for %%%c: %w", verb, err)
				}
				fill := byte(' ')
				if zero && !left {
					fill = '0'
				}
				b.WriteString(padField(strconv.FormatInt(n, 10), width, left, fill))
			case 'x', 'X', 'o':
				n, err := strconv.ParseInt(nextArg(), 0, 64)
				if err != nil {
					return "", ai, fmt.Errorf("invalid number for %%%c: %w", verb, err)
				}
				base := 16
				if verb == 'o' {
					base = 8
				}
				t := strconv.FormatInt(n, base)
				if verb == 'X' {
					t = strings.ToUpper(t)
				}
				b.WriteString(padField(t, width, left, ' '))
			case 'q':
				b.WriteString(shellQuote(nextArg()))
			default:
				return "", ai, fmt.Errorf("unsupported format %%%c", verb)
			}
			continue
		}
		if c == '\\' {
			i++
			if i >= len(format) {
				b.WriteByte('\\')
				break
			}
			switch format[i] {
			case 'n':
				b.WriteByte('\n')
				i++
			case 't':
				b.WriteByte('\t')
				i++
			case 'r':
				b.WriteByte('\r')
				i++
			case 'a':
				b.WriteByte('\a')
				i++
			case 'b':
				b.WriteByte('\b')
				i++
			case 'f':
				b.WriteByte('\f')
				i++
			case 'v':
				b.WriteByte('\v')
				i++
			case '\\':
				b.WriteByte('\\')
				i++
			case '0':
				i++
				v := 0
				for n := 0; n < 3 && i < len(format) && format[i] >= '0' && format[i] <= '7'; n++ {
					v = v*8 + int(format[i]-'0')
					i++
				}
				b.WriteByte(byte(v))
			case 'x':
				i++
				v := 0
				for n := 0; n < 2 && i < len(format) && asciiHex(format[i]); n++ {
					v = v*16 + hexNibble(format[i])
					i++
				}
				b.WriteByte(byte(v))
			default:
				b.WriteByte(format[i])
				i++
			}
			continue
		}
		b.WriteByte(c)
		i++
	}
	return b.String(), ai, nil
}

func asciiDigit(c byte) bool { return c >= '0' && c <= '9' }
func asciiHex(c byte) bool {
	return asciiDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}
func hexNibble(c byte) int {
	switch {
	case asciiDigit(c):
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	default:
		return int(c-'A') + 10
	}
}

func padField(s string, width int, left bool, fill byte) string {
	if width <= len(s) {
		return s
	}
	padding := strings.Repeat(string(fill), width-len(s))
	if left {
		return s + padding
	}
	if fill == '0' && len(s) > 0 && (s[0] == '-' || s[0] == '+') {
		return s[:1] + padding + s[1:]
	}
	return padding + s
}

// shellQuote 对齐 GNU printf %q：可打印字符用反斜杠转义，含控制字符时用 $'...'。
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	control := false
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7f {
			control = true
		}
	}
	if control {
		var b strings.Builder
		b.WriteString("$'")
		for i := 0; i < len(s); i++ {
			switch s[i] {
			case '\n':
				b.WriteString(`\n`)
			case '\t':
				b.WriteString(`\t`)
			case '\r':
				b.WriteString(`\r`)
			case '\\':
				b.WriteString(`\\`)
			case '\'':
				b.WriteString(`\'`)
			default:
				if s[i] < 0x20 || s[i] == 0x7f {
					fmt.Fprintf(&b, `\x%02x`, s[i])
				} else {
					b.WriteByte(s[i])
				}
			}
		}
		b.WriteString("'")
		return b.String()
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if asciiDigit(c) || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			c >= 0x80 || strings.ContainsRune("@%+=:,./-_", rune(c)) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('\\')
		b.WriteByte(c)
	}
	return b.String()
}

// biTest 实现 test / [ ... ]：文件测试、字符串测试、整数比较与 ! 取反。
func (it *Interp) biTest(name string, args []string) (string, string, error) {
	if name == "[" {
		if len(args) == 0 || args[len(args)-1] != "]" {
			return it.failCmd("[", 2, fmt.Errorf("missing ']'"))
		}
		args = args[:len(args)-1]
	}
	v, err := it.evalTest(args)
	if err != nil {
		return it.failCmd(name, 2, err)
	}
	if v {
		it.LastStatus = 0
	} else {
		it.LastStatus = 1
	}
	return "", "", nil
}

func (it *Interp) evalTest(args []string) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	negate := false
	if args[0] == "!" {
		negate = true
		args = args[1:]
	}
	if len(args) == 0 {
		return false, fmt.Errorf("missing argument after !")
	}
	var v bool
	switch len(args) {
	case 1:
		v = args[0] != ""
	case 2:
		switch args[0] {
		case "-f", "-d", "-e", "-s":
			abs, err := it.resolvePath(args[1]) // 文件测试同样受工作区边界约束
			if err != nil {
				return false, err
			}
			info, err := os.Stat(abs)
			if err != nil {
				if !os.IsNotExist(err) {
					return false, err
				}
				v = false
			} else {
				switch args[0] {
				case "-f":
					v = info.Mode().IsRegular()
				case "-d":
					v = info.IsDir()
				case "-e":
					v = true
				case "-s":
					v = info.Size() > 0
				}
			}
		case "-z":
			v = args[1] == ""
		case "-n":
			v = args[1] != ""
		default:
			return false, fmt.Errorf("unknown unary operator %q", args[0])
		}
	case 3:
		op := args[1]
		switch op {
		case "=", "==":
			v = args[0] == args[2]
		case "!=":
			v = args[0] != args[2]
		case "-eq", "-ne", "-lt", "-le", "-gt", "-ge":
			a, err1 := strconv.ParseInt(args[0], 10, 64)
			b, err2 := strconv.ParseInt(args[2], 10, 64)
			if err1 != nil || err2 != nil {
				return false, fmt.Errorf("integer expression expected")
			}
			v = compareInt(op, a, b)
		default:
			return false, fmt.Errorf("unknown binary operator %q", op)
		}
	default:
		return false, fmt.Errorf("too many arguments")
	}
	if negate {
		return !v, nil
	}
	return v, nil
}

func compareInt(op string, a, b int64) bool {
	switch op {
	case "-eq":
		return a == b
	case "-ne":
		return a != b
	case "-lt":
		return a < b
	case "-le":
		return a <= b
	case "-gt":
		return a > b
	default:
		return a >= b
	}
}
