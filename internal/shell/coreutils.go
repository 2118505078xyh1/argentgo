package shell

import (
	"fmt"
	"os"
	"strings"
)

// runCoreutil 分派 coreutils 子集。shell 内建由 runBuiltin 处理，二者互不混杂。
func (it *Interp) runCoreutil(name string, args []string, stdin string) (string, string, error) {
	switch name {
	case "ls":
		return it.biLs(args)
	case "cat":
		return it.biCat(args, stdin)
	case "grep":
		return it.biGrep(args, stdin)
	case "wc":
		return it.biWc(args, stdin)
	case "head":
		return it.biHead(args, stdin)
	case "tail":
		return it.biTail(args, stdin)
	case "mkdir":
		return it.biMkdir(args)
	case "rm":
		return it.biRm(args)
	case "printf":
		return it.biPrintf(args)
	case "test", "[":
		return it.biTest(name, args)
	case "touch":
		return it.biTouch(args)
	case "cp":
		return it.biCp(args)
	case "mv":
		return it.biMv(args)
	case "tee":
		return it.biTee(args, stdin)
	}
	return "", fmt.Sprintf("shell: %s: command not found\n", name), nil
}

// parsedFlags 是短选项解析结果。flags 的键是选项字符，值只用于带参数的选项；
// 选项是否出现由 hasFlag 判断，从而区分空参数与未出现。
type parsedFlags struct {
	flags    map[byte]string
	operands []string
}

func hasFlag(p parsedFlags, c byte) bool {
	_, ok := p.flags[c]
	return ok
}

// parseShortFlags 解析 GNU 风格短选项簇（-laR、-n3、-n 3）。
// 未列出的选项与长选项一律报错，绝不静默忽略。
func parseShortFlags(args []string, allowed, needsArg string) (parsedFlags, error) {
	p := parsedFlags{flags: make(map[byte]string)}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			p.operands = append(p.operands, args[i+1:]...)
			return p, nil
		case a == "-" || !strings.HasPrefix(a, "-"):
			p.operands = append(p.operands, a)
		case strings.HasPrefix(a, "--"):
			return p, fmt.Errorf("unsupported long option %q", a)
		default:
			cluster := a[1:]
			for j := 0; j < len(cluster); j++ {
				c := cluster[j]
				if !strings.Contains(allowed, string(c)) {
					return p, fmt.Errorf("invalid option -- '%c'", c)
				}
				if !strings.Contains(needsArg, string(c)) {
					p.flags[c] = ""
					continue
				}
				value := cluster[j+1:]
				if value == "" {
					if i+1 >= len(args) {
						return p, fmt.Errorf("option requires an argument -- '%c'", c)
					}
					i++
					value = args[i]
				}
				p.flags[c] = strings.TrimPrefix(value, "=")
				break
			}
		}
	}
	return p, nil
}

// failCmd 把命令错误转成可读 stderr + 非零 LastStatus；不返回 Go error，
// 与 shell 的"退出码是结果而非异常"语义一致。
func (it *Interp) failCmd(cmd string, status int, err error) (string, string, error) {
	it.LastStatus = status
	return "", fmt.Sprintf("shell: %s: %v\n", cmd, err), nil
}

// readFileArg 先经 resolvePath 校验工作区边界，再读文件。
func (it *Interp) readFileArg(p, cmd string) (string, error) {
	abs, err := it.resolvePath(p)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", fmt.Errorf("%s: %w", p, err)
	}
	return string(data), nil
}

// splitLines 去掉行尾换行返回行内容；空输入返回 nil，末尾换行不产生额外的空行。
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// splitLinesKeepEnd 保留每行的尾随换行；末行无换行时不补，便于 head/tail 逐字节拼接。
func splitLinesKeepEnd(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.SplitAfter(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
