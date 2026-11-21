package agent

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ErrCommandOutput 表示命令替换回调执行失败。调用方可用 errors.Is 匹配。
// 出现该错误时，对应的 $(cmd) 片段会原样保留在返回字符串中，便于调用方决定如何呈现。
var ErrCommandOutput = errors.New("command substitution failed")

// maxExpandedOutput 限制展开结果的最大字节数。变量、状态与命令输出都经过此上限，
// 以避免恶意回调或超长变量造成"展开炸弹"。截断总是在 rune 边界进行，
// 不会把一个 UTF-8 字符切成两半。
const maxExpandedOutput = 64 * 1024

// ExpansionRequest 是 ExpandCommand 的输入。
//
// Text 是待展开的文本；Vars 提供 $VAR / ${VAR} 的取值（缺失的键视为空串）；
// LastStatus 用于 $?；CommandOutput 是命令替换 $(cmd) 的回调——本包不执行进程，
// 由注入方决定命令如何运行。该回调为 nil 时，$(cmd) 片段原样保留且不报错。
type ExpansionRequest struct {
	Text          string
	Vars          map[string]string
	LastStatus    int
	CommandOutput func(cmd string) (string, error)
}

// expander 持有 ExpandCommand 执行期间的临时状态。它不是导出的：
// 每次调用创建新实例，因此 ExpandCommand 对入参 req（包括 Vars）只读、不做修改。
type expander struct {
	req      ExpansionRequest
	out      strings.Builder
	bytes    int
	firstErr error
}

// write 把 s 追加到结果中，但保证总字节数不超过 maxExpandedOutput。
// 超出时按 rune 边界截断，绝不产生非法的 UTF-8 片段。
func (e *expander) write(s string) {
	remaining := maxExpandedOutput - e.bytes
	if remaining <= 0 {
		return
	}
	if len(s) <= remaining {
		e.out.WriteString(s)
		e.bytes += len(s)
		return
	}
	cut := s[:remaining]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	e.out.WriteString(cut)
	e.bytes += len(cut)
}

// expandCommand 处理 $(cmd)：回调为空则原样保留；回调失败则记录首个错误并原样保留；
// 成功时用输出替换。嵌套的 $(...) 不在本层递归展开，而是把原始命令整体交给回调，
// 由 findCommandEnd 负责括号配平。
func (e *expander) expandCommand(cmd string) {
	literal := "$(" + cmd + ")"
	if e.req.CommandOutput == nil {
		e.write(literal)
		return
	}
	out, err := e.req.CommandOutput(cmd)
	if err != nil {
		if e.firstErr == nil {
			e.firstErr = fmt.Errorf("%w: cmd %q: %v", ErrCommandOutput, cmd, err)
		}
		e.write(literal)
		return
	}
	e.write(out)
}

// isVarStart / isVarChar 界定裸 $VAR 的 ASCII 变量名规则；
// ${VAR} 形式则把直到 '}' 的全部内容当作键，因此支持非 ASCII 变量名。
func isVarStart(c byte) bool {
	return c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

func isVarChar(c byte) bool {
	return isVarStart(c) || (c >= '0' && c <= '9')
}

// findCommandEnd 返回与 start 处（即 $( 之后）匹配的 ')' 下标。
// 它统计嵌套括号深度，并忽略单/双引号及反斜杠转义中的括号，
// 使 $(echo (a)) 与 $(echo "$(x)") 都能正确定位结束位置。
func findCommandEnd(s string, start int) (int, bool) {
	depth := 1
	quote := byte(0)
	for i := start; i < len(s); {
		c := s[i]
		if c == '\\' && quote != '\'' {
			if i+1 < len(s) {
				_, size := utf8.DecodeRuneInString(s[i+1:])
				i += 1 + size
			} else {
				i++
			}
			continue
		}
		if c == '\'' && quote != '"' {
			if quote == '\'' {
				quote = 0
			} else {
				quote = '\''
			}
			i++
			continue
		}
		if c == '"' && quote != '\'' {
			if quote == '"' {
				quote = 0
			} else {
				quote = '"'
			}
			i++
			continue
		}
		if quote == 0 {
			switch c {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					return i, true
				}
			}
		}
		i++
	}
	return 0, false
}

// ExpandCommand 扫描 req.Text 并展开四类片段：
//
//	$VAR / ${VAR}  从 req.Vars 取值，缺失的键为空串
//	$?             req.LastStatus 的十进制字符串
//	$(cmd)         调用 req.CommandOutput(cmd)，以其输出替换
//
// 规则：单引号内不展开（反斜杠在单引号内也是字面量）；双引号内与引号外正常展开；
// 反斜杠转义下一个 rune（去掉反斜杠本身）。未闭合的 ${ 与 $( 按字面 '$' 处理。
// 回调为 nil 时 $(cmd) 原样保留且不报错；回调返回错误时原样保留并返回包装后的
// ErrCommandOutput（可用 errors.Is 匹配），且继续处理后续文本（仅记录首个错误）。
//
// 该函数是纯函数：不修改 req 及其 Vars，不做任何 I/O，进程执行由注入的回调负责。
// 输出受 maxExpandedOutput 限制，按 rune 边界截断，CJK 安全。
func ExpandCommand(req ExpansionRequest) (string, error) {
	e := &expander{req: req}
	s := req.Text
	quote := byte(0)

	for i := 0; i < len(s); {
		c := s[i]

		// 引号状态：单引号抑制一切展开，双引号内仍展开。
		if c == '\'' && quote != '"' {
			if quote == '\'' {
				quote = 0
			} else {
				quote = '\''
			}
			e.write("'")
			i++
			continue
		}
		if c == '"' && quote != '\'' {
			if quote == '"' {
				quote = 0
			} else {
				quote = '"'
			}
			e.write("\"")
			i++
			continue
		}

		// 反斜杠转义：单引号内是字面量，其余位置吞掉反斜杠、原样写入下一个 rune。
		if c == '\\' && quote != '\'' {
			if i+1 < len(s) {
				_, size := utf8.DecodeRuneInString(s[i+1:])
				e.write(s[i+1 : i+1+size])
				i += 1 + size
			} else {
				e.write("\\")
				i++
			}
			continue
		}

		// 美元展开仅在非单引号上下文生效。
		if c == '$' && quote != '\'' && i+1 < len(s) {
			switch s[i+1] {
			case '?':
				e.write(strconv.Itoa(req.LastStatus))
				i += 2
				continue
			case '{':
				if end := strings.IndexByte(s[i+2:], '}'); end >= 0 {
					e.write(req.Vars[s[i+2:i+2+end]])
					i += 2 + end + 1
					continue
				}
			case '(':
				if end, ok := findCommandEnd(s, i+2); ok {
					e.expandCommand(s[i+2 : end])
					i = end + 1
					continue
				}
			default:
				if isVarStart(s[i+1]) {
					j := i + 1
					for j < len(s) && isVarChar(s[j]) {
						j++
					}
					e.write(req.Vars[s[i+1:j]])
					i = j
					continue
				}
			}
		}

		// 默认：按完整 rune 拷贝，保证 CJK 不被拆开。
		_, size := utf8.DecodeRuneInString(s[i:])
		e.write(s[i : i+size])
		i += size
	}

	return e.out.String(), e.firstErr
}
