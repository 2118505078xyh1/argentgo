package bash

// ShellFeature 记录一条命令中出现的 shell 元字符特性。
// 它是纯词法扫描的产物，供调用方判断命令是否需要交给 shell 解释执行。
//
// 本文件是一组纯函数、零调用者的检测原语：它不改变任何现有工具的
// Specs()/Run() 行为，只提供统一且保守的元字符识别标准。
type ShellFeature struct {
	HasPipe      bool // 管道 |
	HasAnd       bool // 逻辑与 &&
	HasOr        bool // 逻辑或 ||
	HasRedirect  bool // 重定向 > >> < 以及 2>&1
	HasGlob      bool // 通配符 * ? [
	HasVar       bool // 变量展开 $VAR $? ${VAR} 等
	HasCmdSubst  bool // 命令替换 $(
	HasSemicolon bool // 分号 ;
}

// DetectShellFeatures 对 command 做一次纯词法扫描，报告其中出现的 shell
// 元字符特性。它不执行命令、不做 I/O、也不修改入参（Go 字符串本身不可变）。
//
// 引号与转义规则（三者都会让元字符退化成字面量，因而不会被计入）：
//   - 单引号内一切都是字面量，只有闭合的 ' 能退出；单引号内反斜杠不转义。
//   - 双引号内元字符同样按字面量处理，只有闭合的 " 能退出；其中 \X 会
//     吃掉紧跟的字符，使得 \" 不会被误当成闭合引号。
//   - 普通态下反斜杠转义紧随其后的一个字节（如 \| \* \$）。
//
// CJK 安全：只有 ASCII 字节才可能是元字符，UTF-8 多字节序列的续字节均
// 大于等于 0x80，扫描时按字节前进不会把它们误判为元字符，也不会拆分或
// 改写任何 rune。
//
// 这是一份保守的启发式检测而非完整的 shell 解析器：它只关心"是否出现了
// 需要 shell 解释的元字符"，不校验语法，也不区分内建命令与外部命令。
func DetectShellFeatures(command string) ShellFeature {
	var f ShellFeature

	const (
		stateNormal = iota
		stateSingle
		stateDouble
	)
	state := stateNormal

	for i := 0; i < len(command); {
		c := command[i]

		// 单引号：一切都是字面量，反斜杠在此不具转义含义。
		if state == stateSingle {
			if c == '\'' {
				state = stateNormal
			}
			i++
			continue
		}

		// 反斜杠转义：吃掉紧随其后的一个字节，使元字符失去意义。
		// 单引号已在上分支处理，这里覆盖普通态与双引号态。
		if c == '\\' && i+1 < len(command) {
			i += 2
			continue
		}

		// 双引号：元字符按字面量处理，只寻找闭合引号。
		if state == stateDouble {
			if c == '"' {
				state = stateNormal
			}
			i++
			continue
		}

		// 普通态：逐个识别元字符。
		switch c {
		case '\'':
			state = stateSingle
			i++
		case '"':
			state = stateDouble
			i++
		case '|':
			if i+1 < len(command) && command[i+1] == '|' {
				f.HasOr = true
				i += 2
			} else {
				f.HasPipe = true
				i++
			}
		case '&':
			if i+1 < len(command) && command[i+1] == '&' {
				f.HasAnd = true
				i += 2
			} else {
				// 单个 & 是后台运行符，本原语的字段清单不含它，忽略。
				i++
			}
		case '>', '<':
			// >> 会被扫两次，但 HasRedirect 是布尔量，重复标记无害。
			// 2>&1 中的 > 也在此被捕获，其后的单个 & 不会产生 HasAnd。
			f.HasRedirect = true
			i++
		case '*', '?', '[':
			f.HasGlob = true
			i++
		case ';':
			f.HasSemicolon = true
			i++
		case '$':
			if i+1 >= len(command) {
				i++
				break
			}
			n := command[i+1]
			switch {
			case n == '(':
				f.HasCmdSubst = true
				i += 2
			case isShellVarByte(n) || n == '{' || isShellSpecialParam(n):
				f.HasVar = true
				// 连同后继字节一起消费，避免 $* 中的 * 又被当成通配符、
				// $? 中的 ? 又被当成通配符。
				i += 2
			default:
				i++
			}
		default:
			// 非 ASCII 字节（CJK/emoji 等的 UTF-8 字节）与其余字面量
			// 都不可能是 ASCII 元字符，单个字节前进即可。
			i++
		}
	}
	return f
}

// isShellVarByte 报告 c 是否可作为 shell 变量名中的字符（字母、数字或下划线）。
func isShellVarByte(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// isShellSpecialParam 报告 c 是否是 $ 之后需要展开的特殊参数字符。
func isShellSpecialParam(c byte) bool {
	switch c {
	case '?', '!', '#', '@', '*', '$', '-':
		return true
	}
	return c >= '0' && c <= '9'
}

// NeedsShell 报告任一 shell 元字符特性是否出现。
// 只要有任意一项为真，命令就应当交给 shell 解释执行。
func NeedsShell(f ShellFeature) bool {
	return f.HasPipe || f.HasAnd || f.HasOr || f.HasRedirect ||
		f.HasGlob || f.HasVar || f.HasCmdSubst || f.HasSemicolon
}

// FeatureList 按稳定顺序返回 f 中为真特性的中文名称。
// 顺序固定为：管道、逻辑与、逻辑或、重定向、通配符、变量展开、命令替换、分号，
// 与 ShellFeature 的字段顺序一致。没有任何特性时返回长度为 0 的切片（nil）。
func FeatureList(f ShellFeature) []string {
	var out []string
	if f.HasPipe {
		out = append(out, "管道")
	}
	if f.HasAnd {
		out = append(out, "逻辑与")
	}
	if f.HasOr {
		out = append(out, "逻辑或")
	}
	if f.HasRedirect {
		out = append(out, "重定向")
	}
	if f.HasGlob {
		out = append(out, "通配符")
	}
	if f.HasVar {
		out = append(out, "变量展开")
	}
	if f.HasCmdSubst {
		out = append(out, "命令替换")
	}
	if f.HasSemicolon {
		out = append(out, "分号")
	}
	return out
}
