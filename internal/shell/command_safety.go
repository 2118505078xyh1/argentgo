package shell

import (
	"errors"
	"fmt"
	"strings"
)

// 命令安全性诊断原语。本文件只新增"纯检测 + 脱敏 + 哨兵错误"三类能力，
// 默认不接入任何工具的 Specs()/Run() 路径：调用方按需 opt-in 使用。
//
// 它与 shell_detect.go 的分工是：后者只报告"出现了哪些 shell 元字符"，
// 而本文件额外回答两个问题——这条命令能否被内建解释器安全执行（Supported），
// 以及它属于哪一类风险（Risk）。所有判断都是纯函数，不执行命令、不做 I/O、
// 不修改入参，UTF-8/CJK 文本按字节原样透传，绝不在 rune 中间截断。

// Risk 风险分类的取值。它们按"主要风险"的优先级排列，但 Reasons 会列出
// 全部检出的风险来源，因此单个 Risk 只是最显著的那一项。
const (
	RiskSafe         = "safe"          // 内建支持且无任何风险元字符
	RiskForeign      = "foreign"       // 内建不支持的构造（循环/函数/子 shell/算术/后台等）
	RiskCommandSubst = "command-subst" // 命令替换 $(...)
	RiskRedirect     = "redirect"      // 重定向 > >> < 2>&1
	RiskGlob         = "glob"          // 通配符 * ? [
	RiskCompound     = "compound"      // 复合命令 | && || ;
)

// ShellSafety 是一条命令的静态安全性诊断结果。
//   - Supported：内建解释器能否解析并执行该命令（复用 shell.go 的 Supported）。
//   - Risk：主要风险类别，取值见上方 Risk* 常量。
//   - Reasons：全部风险来源的中文描述；Supported=false 时至少包含不支持的原因。
type ShellSafety struct {
	Supported bool
	Risk      string
	Reasons   []string
}

// ErrUnsupportedCommand 是 RequireSafeCommand 的哨兵错误：命令不在内建
// 解释器支持的语法子集内。调用方可用 errors.Is 匹配；错误文本附带原因。
var ErrUnsupportedCommand = errors.New("shell: unsupported command")

// ClassifyShellCommand 对 command 做静态安全性诊断。
//
// 判定流程：
//  1. 用现有 lexer/parser（Supported）判断命令是否在内建语法子集内；
//  2. 用 DetectShellFeatures 标注命中的风险元字符（作为 Reasons）；
//  3. 选出一个主要 Risk：不支持 => foreign；否则按命令替换、重定向、
//     通配符、复合命令的优先级取最高者；都没有则为 safe。
//
// 约定：Supported=false 时 Risk 至少是 foreign（本实现固定为 foreign），
// 绝不会降级成 safe/redirect/glob。Supported=true 的命令即使带重定向或
// 通配符也仍是可执行的——那些属于内建支持的语法，只在 Reasons 里提示。
//
// 本函数是纯函数：不执行命令、不做 I/O、不修改入参，CJK 安全。
func ClassifyShellCommand(command string) ShellSafety {
	supported := Supported(command)
	f := DetectShellFeatures(command)

	risk := RiskSafe
	switch {
	case !supported:
		// 不支持的构造对内建解释器而言是"外来语法"，固定标为 foreign；
		// 具体构造名由 Reasons 给出（复合特征也会一并列出）。
		risk = RiskForeign
	case f.HasCmdSubst:
		risk = RiskCommandSubst
	case f.HasRedirect:
		risk = RiskRedirect
	case f.HasGlob:
		risk = RiskGlob
	case f.HasPipe || f.HasAnd || f.HasOr || f.HasSemicolon:
		risk = RiskCompound
	}

	reasons := classifyReasons(command, supported, f)
	return ShellSafety{Supported: supported, Risk: risk, Reasons: reasons}
}

// classifyReasons 汇总不支持原因与命中的元字符名称，顺序稳定：
// 先列"不支持的构造/保留字/语法错误"，再按 FeatureList 的固定顺序列元字符。
func classifyReasons(command string, supported bool, f ShellFeature) []string {
	var reasons []string
	if !supported {
		reasons = append(reasons, unsupportedReasons(command)...)
	}
	return append(reasons, FeatureList(f)...)
}

// unsupportedReasons 重新走一遍 lexer/parser，收集命令为何不被支持的中文原因。
// 这里复用现有词法/语法分析器，不修改它们一行代码。
func unsupportedReasons(command string) []string {
	tokens, err := newLexer(command).lex()
	if err != nil {
		return []string{"词法错误: " + err.Error()}
	}
	f, err := parse(tokens)
	if err != nil {
		return []string{"语法错误: " + err.Error()}
	}
	var out []string
	collectUnsupportedReasons(f.Stmt, &out)
	if len(out) == 0 {
		out = append(out, "不受内建解释器支持的构造")
	}
	return out
}

// collectUnsupportedReasons 遍历 AST，把 UnsupportedCmd 的构造名与用作命令的
// 保留字记入 out（去重）。遍历结构与 shell.go 的 containsUnsupported 一致。
func collectUnsupportedReasons(st *Stmt, out *[]string) {
	if st == nil {
		return
	}
	switch c := st.Cmd.(type) {
	case *UnsupportedCmd:
		appendReason(out, "不支持的构造: "+c.Kind)
	case *BinaryCmd:
		collectUnsupportedReasons(c.X, out)
		collectUnsupportedReasons(c.Y, out)
	case *CallExpr:
		if len(c.Args) > 0 && len(c.Args[0].Parts) > 0 {
			if lit, ok := c.Args[0].Parts[0].(*LitPart); ok && isReservedWord(lit.Text) {
				appendReason(out, "不支持的保留字: "+lit.Text)
			}
		}
	}
}

// appendReason 追加原因字符串，避免同一构造重复出现。
func appendReason(out *[]string, reason string) {
	for _, r := range *out {
		if r == reason {
			return
		}
	}
	*out = append(*out, reason)
}

// RequireSafeCommand 在命令不被内建解释器支持时返回带原因的哨兵错误；
// 支持时一律返回 nil——重定向、通配符、命令替换都属于内建支持的语法，
// 不在此处拒绝（它们只会被 ClassifyShellCommand 标注风险）。
//
// 用法：
//
//	if err := RequireSafeCommand(cmd); err != nil {
//	    if errors.Is(err, ErrUnsupportedCommand) { /* 回退到系统 shell 或报错 */ }
//	}
func RequireSafeCommand(command string) error {
	safety := ClassifyShellCommand(command)
	if safety.Supported {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrUnsupportedCommand, strings.Join(safety.Reasons, "; "))
}

// SanitizeForLog 把命令文本脱敏成适合日志/审计的"单行安全形状"：
//
//   - 控制字符（含换行、回车、制表）统一改成空格，连续空格折叠为一个并去掉首尾空格，
//     保证输出是单行；
//   - 变量/参数展开（$VAR、${VAR}、${VAR:-def}、$?、$1 等）替换为 $<redacted>
//     或 ${<redacted>}，抹掉变量名与其默认值；
//   - 赋值语句 VAR=value（含被引号包裹的值）把 value 替换为 <redacted>，
//     抹掉"变量的值"；
//   - 引号内的敏感串（含敏感关键字，或形似口令的字母数字混合串）只保留引号形状，
//     内容替换为 <redacted>；
//   - $(...) 保留外壳继续向内扫描，使嵌套的变量展开同样被脱敏。
//
// 单引号内的 $ 按字面量处理（与 shell 语义一致）。所有 >= 0x80 的字节
// （包括 CJK/emoji 的 UTF-8 字节）原样透传，绝不在 rune 中间截断；函数不修改入参。
func SanitizeForLog(command string) string {
	var out strings.Builder
	out.Grow(len(command))

	inSingle, inDouble := false, false
	atWordStart := true // 是否处于单词起始，用于识别 VAR=value 赋值

	for i := 0; i < len(command); {
		c := command[i]

		// 控制字符（含换行）折叠成空格，保证单行；同时开启一个新单词。
		if c < 0x20 || c == 0x7f {
			out.WriteByte(' ')
			atWordStart = true
			i++
			continue
		}

		// 单引号内一切按字面量处理，只有闭合引号退出。
		if inSingle {
			out.WriteByte(c)
			if c == '\'' {
				inSingle = false
			}
			i++
			continue
		}

		// 双引号内：处理变量展开与闭合引号，敏感串在入口处已整体脱敏。
		if inDouble {
			if c == '"' {
				out.WriteByte('"')
				inDouble = false
				i++
				continue
			}
			if c == '$' {
				var used int
				out.WriteString(redactDollar(command, i, &used))
				i += used
				continue
			}
			out.WriteByte(c)
			i++
			continue
		}

		// 普通态。
		switch {
		case c == '\'':
			// 先判断整段单引号内容是否敏感，敏感则只保留引号形状。
			if content, end, ok := quotedContent(command, i, '\''); ok && quotedLooksSensitive(content) {
				out.WriteByte('\'')
				out.WriteString("<redacted>")
				out.WriteByte('\'')
				i = end
			} else {
				inSingle = true
				out.WriteByte(c)
				i++
			}
			atWordStart = false

		case c == '"':
			if content, end, ok := quotedContent(command, i, '"'); ok && quotedLooksSensitive(content) {
				out.WriteByte('"')
				out.WriteString("<redacted>")
				out.WriteByte('"')
				i = end
			} else {
				inDouble = true
				out.WriteByte(c)
				i++
			}
			atWordStart = false

		case c == '\\':
			// 转义：保留反斜杠与其后一个字节（控制字符仍规范化为空格）。
			out.WriteByte(c)
			i++
			if i < len(command) {
				n := command[i]
				if n < 0x20 || n == 0x7f {
					out.WriteByte(' ')
				} else {
					out.WriteByte(n)
				}
				i++
			}
			atWordStart = false

		case c == '$':
			var used int
			out.WriteString(redactDollar(command, i, &used))
			i += used
			atWordStart = false

		case isAssignStart(c) && atWordStart:
			// 尝试解析 NAME=... 赋值（NAME 仅允许 ASCII 标识符字节）。
			j := i
			for j < len(command) && isAssignByte(command[j]) {
				j++
			}
			if j > i && j < len(command) && command[j] == '=' {
				out.WriteString(command[i:j])
				out.WriteByte('=')
				i = redactAssignValue(command, j+1, &out)
				atWordStart = false
				continue
			}
			out.WriteByte(c)
			i++
			atWordStart = false

		default:
			out.WriteByte(c)
			i++
			atWordStart = c == ' ' // 只有普通空格开启新单词
		}
	}

	return collapseAndTrim(out.String())
}

// redactDollar 从 command[i]=='$' 处解析一个展开并写入脱敏形状，
// 返回写入的文本与消耗的字节数 used。
func redactDollar(command string, i int, used *int) string {
	if i+1 >= len(command) {
		*used = 1
		return "$"
	}
	n := command[i+1]
	switch {
	case n == '(':
		// 命令替换/算术：只吞掉 "$(，内部内容交回主循环继续脱敏）。
		*used = 2
		return "$("
	case n == '{':
		// ${VAR} / ${VAR:-def}：吞到配对的 '}'（支持嵌套花括号）。
		j := i + 2
		depth := 1
		for j < len(command) && depth > 0 {
			switch command[j] {
			case '{':
				depth++
			case '}':
				depth--
			}
			j++
		}
		*used = j - i
		return "${<redacted>}"
	case isShellVarByte(n) || isShellSpecialParam(n):
		// $VAR / $? / $1 / $* 等：吞掉变量名或单个特殊参数字符。
		j := i + 2
		if isShellVarByte(n) {
			for j < len(command) && isShellVarByte(command[j]) {
				j++
			}
		}
		*used = j - i
		return "$<redacted>"
	default:
		// 孤立的 '$'（如 "$5" 中的字面量）按原样保留。
		*used = 1
		return "$"
	}
}

// redactAssignValue 从 valueStart 处吞掉一个赋值值并写入 <redacted>（保留引号
// 形状），返回紧随其后的下标。到下一个空白或未闭合的引号处结束。
func redactAssignValue(command string, valueStart int, out *strings.Builder) int {
	if valueStart >= len(command) {
		out.WriteString("<redacted>")
		return valueStart
	}
	if q := command[valueStart]; q == '\'' || q == '"' {
		out.WriteByte(q)
		out.WriteString("<redacted>")
		out.WriteByte(q)
		if _, end, ok := quotedContent(command, valueStart, q); ok {
			return end
		}
		return len(command)
	}
	j := valueStart
	for j < len(command) && command[j] > ' ' && command[j] != 0x7f {
		j++
	}
	out.WriteString("<redacted>")
	return j
}

// quotedContent 返回从 command[i]==quote 开始的引号内容、闭合引号之后的下标，
// 以及是否找到闭合引号。双引号内反斜杠转义下一个字节；单引号内无转义。
func quotedContent(command string, i int, quote byte) (content string, end int, ok bool) {
	if i >= len(command) || command[i] != quote {
		return "", i, false
	}
	j := i + 1
	start := j
	for j < len(command) {
		c := command[j]
		if c == '\\' && quote == '"' && j+1 < len(command) {
			j += 2
			continue
		}
		if c == quote {
			return command[start:j], j + 1, true
		}
		j++
	}
	return command[start:], len(command), false
}

// sensitiveKeywords 是判断"引号内字符串是否敏感"的关键字（小写）。
var sensitiveKeywords = []string{
	"password", "passwd", "secret", "token", "credential",
	"apikey", "api_key", "api-key", "auth", "bearer", "private",
}

// quotedLooksSensitive 报告引号内容是否形似敏感串：含敏感关键字，或是
// 长度 >= 6 且同时含 ASCII 字母与数字的口令状混合串。CJK 文本不会因字节
// 组合被误判——只有 ASCII 字母/数字参与"混合串"判定。
func quotedLooksSensitive(content string) bool {
	lower := strings.ToLower(content)
	for _, kw := range sensitiveKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	var hasLetter, hasDigit bool
	for i := 0; i < len(content); i++ {
		c := content[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			hasLetter = true
		} else if c >= '0' && c <= '9' {
			hasDigit = true
		}
	}
	return hasLetter && hasDigit && len(content) >= 6
}

// isAssignStart 报告 c 能否作为赋值变量名（= 左侧）的首字节。
func isAssignStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// isAssignByte 报告 c 能否作为赋值变量名的后续字节。
func isAssignByte(c byte) bool {
	return isAssignStart(c) || (c >= '0' && c <= '9')
}

// collapseAndTrim 把 ASCII 空格折叠为单个并去掉首尾空格。按字节处理，
// >= 0x80 的 UTF-8 字节原样保留，因此 CJK/emoji 不会被拆分；首尾也只裁掉
// ASCII 空格，全角空格等 CJK 空白不会被误删。
func collapseAndTrim(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	pending := false // 是否有一个待定空格（仅当后面还有内容时才落下）
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ' ' {
			if b.Len() > 0 {
				pending = true
			}
			continue
		}
		if pending {
			b.WriteByte(' ')
			pending = false
		}
		b.WriteByte(c)
	}
	return b.String()
}
