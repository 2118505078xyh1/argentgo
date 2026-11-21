package shell

import "strings"

// 命令风险分级原语。本文件只提供"静态检测 + 分级"，不拦截、不执行、不做 I/O、
// 不修改入参，供上层（审批/审计/UI）作为参考信号按需使用，默认不接入任何执行路径。
//
// ⚠️ 重要：本原语只作 UX 护栏（给用户一句"这条命令看起来危险"的提示），
// 绝不是安全边界。它基于字符串静态匹配，可被编码、变量拼接、bash -c 包裹等
// 手段绕过，因此绝不能替代真正的安全策略、审批或沙箱。
//
// 设计参照 internal/security 的 command_guard：先 mask 引号字符串、反引号替换、
// shell 注释与 heredoc 正文，再做 token 级匹配，因此字符串字面量里的
// "rm -rf" 不会被误报。所有判断都是纯函数，UTF-8/CJK 按字节原样透传，
// 结果确定，且不保留任何全局可变状态。

// RiskLevel 是命令的风险等级，数值越大越危险。
type RiskLevel int

const (
	riskNone   RiskLevel = iota // 未检出危险模式
	riskLow                     // 轻度：普通删除/改名/改权等可恢复操作
	riskMedium                  // 中度：覆盖重定向、打印密钥、放宽权限等
	riskHigh                    // 高度：递归强删、格式化、管道进 shell、提权等
)

// String 返回稳定的英文等级名，便于审计/日志显示。
func (r RiskLevel) String() string {
	switch r {
	case riskNone:
		return "none"
	case riskLow:
		return "low"
	case riskMedium:
		return "medium"
	case riskHigh:
		return "high"
	default:
		return "unknown"
	}
}

// AssessCommandRisk 对 cmd 做静态风险分级。
//
// 判定流程：
//  1. mask 引号/反引号/注释/heredoc 正文，避免字符串字面量误报；
//  2. 转为小写并拆成以管道/分号/换行等为边界的命令段，记录每段是否紧跟管道；
//  3. 按 high → medium → low 的优先级取最高命中等级，全不命中为 riskNone。
//
// 本函数是纯函数：不执行命令、不做 I/O、不修改入参（Go 字符串不可变），
// CJK/UTF-8 字节原样处理，绝不在 rune 中间截断；相同输入恒得相同输出。
func AssessCommandRisk(cmd string) RiskLevel {
	if strings.TrimSpace(cmd) == "" {
		return riskNone
	}
	lower := strings.ToLower(maskCommandForRisk(cmd))
	segments := riskSegments(lower)

	switch {
	case hasHighRisk(segments):
		return riskHigh
	case hasMediumRisk(lower, segments):
		return riskMedium
	case hasLowRisk(segments):
		return riskLow
	default:
		return riskNone
	}
}

// riskSegment 是一个逻辑命令段。afterPipe 表示该段紧跟在管道符之后，
// 用于识别 "... | sh" 这类"管道到 shell"的高危构造。
type riskSegment struct {
	words     []string
	afterPipe bool
}

// hasHighRisk 检测高危模式：递归强删（rm -rf / del /f / Remove-Item -Recurse -Force）、
// 格式化/磁盘擦除（mkfs、format、dd、shred）、管道到 shell（含 curl|sh）、权限提升
// （sudo/su/runas 等）。命中任一即判为高危。
func hasHighRisk(segments []riskSegment) bool {
	for _, seg := range segments {
		// 提权词可以在包装位置出现（如 `sudo -u root ...`），扫描整段。
		for _, w := range seg.words {
			if isPrivilegeCommand(baseName(w)) {
				return true
			}
		}

		cmd := commandWord(seg.words)
		if cmd == "" {
			continue
		}
		base := baseName(cmd)
		switch base {
		case "mkfs", "mkfs.ext4", "mkfs.xfs", "mke2fs", "newfs", "format", "diskpart", "dd", "shred":
			return true
		}
		if isDestructiveDelete(seg.words, base) {
			return true
		}
		if seg.afterPipe && isShellName(base) {
			return true
		}
	}
	return false
}

// hasMediumRisk 检测中危模式：覆盖重定向 `>`（区别于 >> 追加与 2>&1）、
// 打印全部/敏感环境变量（env、printenv、echo $SECRET）、放宽权限（chmod 777）。
func hasMediumRisk(lower string, segments []riskSegment) bool {
	if hasOverwriteRedirect(lower) {
		return true
	}
	for _, seg := range segments {
		cmd := commandWord(seg.words)
		// 裸 env/printenv/set 会转储整个环境，可能含口令；带参数的
		// `env FOO=1 cmd` 则是设置变量后执行，不属于打印密钥。
		// 这里直接看首词——commandWord 会把 env 当透明包装器跳过，
		// 而裸 env 本身正是需要检出的对象。
		if len(seg.words) == 1 {
			first := seg.words[0]
			if first == "env" || first == "printenv" || first == "set" {
				return true
			}
		}
		if cmd == "chmod" && hasWorldWritable(seg.words) {
			return true
		}
		for _, w := range seg.words {
			if containsSecretVarRef(w) {
				return true
			}
		}
	}
	return false
}

// hasLowRisk 检测轻度风险：不带 -rf 的普通删除、改名、改权等。
func hasLowRisk(segments []riskSegment) bool {
	for _, seg := range segments {
		base := baseName(commandWord(seg.words))
		switch base {
		case "rm", "rmdir", "unlink", "del", "erase", "mv", "move",
			"chmod", "chown", "attrib", "icacls", "remove-item", "ri":
			return true
		}
	}
	return false
}

// commandWord 返回命令段的真正可执行文件名：跳过 VAR=value 赋值与常见的
// 透明包装器（env/nohup/...）。sudo 刻意不列入包装器——它本身就该触发提权。
func commandWord(words []string) string {
	for _, w := range words {
		if isRiskAssignment(w) {
			continue
		}
		switch w {
		case "env", "nohup", "command", "builtin", "exec", "time":
			continue
		}
		return w
	}
	return ""
}

// isRiskAssignment 报告 w 是否为纯 ASCII 标识符命名的 VAR=value 赋值。
func isRiskAssignment(w string) bool {
	eq := strings.IndexByte(w, '=')
	if eq <= 0 {
		return false
	}
	for i := 0; i < eq; i++ {
		c := w[i]
		if !isAlnumByte(c) && c != '_' {
			return false
		}
	}
	return true
}

// baseName 去掉路径前缀与 .exe 后缀，得到可比较的命令名。
func baseName(w string) string {
	if i := strings.LastIndexByte(w, '/'); i >= 0 {
		w = w[i+1:]
	}
	if i := strings.LastIndexByte(w, '\\'); i >= 0 {
		w = w[i+1:]
	}
	return strings.TrimSuffix(w, ".exe")
}

// isPrivilegeCommand 报告命令是否为权限提升工具。
func isPrivilegeCommand(name string) bool {
	switch name {
	case "sudo", "su", "runas", "doas", "pkexec":
		return true
	}
	return false
}

// isShellName 报告命令是否为可被管道喂入的 shell。
func isShellName(name string) bool {
	switch name {
	case "sh", "bash", "zsh", "dash", "ksh", "rbash", "ash", "fish":
		return true
	}
	return false
}

// isDestructiveDelete 判断删除命令是否带"递归 + 强制"这类不可逆标志。
func isDestructiveDelete(words []string, cmd string) bool {
	switch cmd {
	case "rm", "rmdir", "unlink":
		// rm -rf / rm -fr / rm -r -f / rm --recursive --force 都算递归强删。
		var recursive, force bool
		for _, w := range words {
			switch {
			case w == "--recursive" || w == "-recursive":
				recursive = true
			case w == "--force" || w == "-force":
				force = true
			case strings.HasPrefix(w, "-") && len(w) > 1:
				for i := 1; i < len(w); i++ {
					switch w[i] {
					case 'r', 'R':
						recursive = true
					case 'f', 'F':
						force = true
					}
				}
			}
		}
		return recursive && force
	case "del", "erase":
		// del /f 强制删除只读文件；/s 递归删除。
		for _, w := range words {
			if strings.HasPrefix(w, "/") && len(w) > 1 {
				if strings.ContainsAny(w, "fsFS") {
					return true
				}
			}
		}
	case "remove-item", "ri":
		// PowerShell: Remove-Item -Recurse -Force。
		var recursive, force bool
		for _, w := range words {
			if strings.HasPrefix(w, "-") {
				switch strings.TrimLeft(w, "-") {
				case "recurse":
					recursive = true
				case "force":
					force = true
				}
			}
		}
		return recursive && force
	}
	return false
}

// hasOverwriteRedirect 报告 lower 中是否存在"覆盖写"重定向 `>`。
// `>>`（追加）、`2>&1`/`>&2`（文件描述符复制）不算覆盖写。
func hasOverwriteRedirect(lower string) bool {
	for i := 0; i < len(lower); i++ {
		if lower[i] != '>' {
			continue
		}
		if i+1 < len(lower) {
			switch lower[i+1] {
			case '>': // >> 追加，跳过这对
				i++
				continue
			case '&': // 2>&1 / >&2，不是文件覆盖
				continue
			}
		}
		return true
	}
	return false
}

// hasWorldWritable 报告 chmod 参数里是否出现 777（对所有人可写）。
func hasWorldWritable(words []string) bool {
	for _, w := range words {
		if w == "777" || w == "a+w" || w == "a+rwx" {
			return true
		}
	}
	return false
}

// secretNameKeywords 是"看起来像密钥"的环境变量名子串（小写）。
var secretNameKeywords = []string{
	"password", "passwd", "secret", "token", "api_key", "apikey",
	"access_key", "private_key", "credential", "auth", "bearer",
	"session", "cookie",
}

// containsSecretVarRef 报告单词 w 中是否引用了形如 $PASSWORD / ${API_KEY}
// 的敏感环境变量。普通变量（$HOME、$?、$1）不会命中。
func containsSecretVarRef(w string) bool {
	for i := 0; i < len(w); i++ {
		if w[i] != '$' {
			continue
		}
		j := i + 1
		braced := false
		if j < len(w) && w[j] == '{' {
			braced = true
			j++
		}
		start := j
		for j < len(w) && (isAlnumByte(w[j]) || w[j] == '_') {
			j++
		}
		if j > start {
			name := w[start:j]
			for _, kw := range secretNameKeywords {
				if strings.Contains(name, kw) {
					return true
				}
			}
		}
		if braced {
			i = j
		} else {
			i = j - 1
		}
	}
	return false
}

// riskSegments 把掩码后的小写命令拆成逻辑段：;、&、|、换行都是段边界，
// 管道后的第一段会被标记 afterPipe。括号、重定向、空白用于单词切分。
func riskSegments(s string) []riskSegment {
	var out []riskSegment
	var cur riskSegment
	var b strings.Builder
	flushWord := func() {
		if b.Len() > 0 {
			cur.words = append(cur.words, b.String())
			b.Reset()
		}
	}
	flush := func() {
		flushWord()
		if len(cur.words) > 0 || cur.afterPipe {
			out = append(out, cur)
		}
		cur = riskSegment{}
	}

	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case ';', '&', '\n', '\r':
			flush()
		case '|':
			flush()
			cur.afterPipe = true
		default:
			if isRiskWordSep(c) {
				flushWord()
			} else {
				b.WriteByte(c)
			}
		}
	}
	flush()
	return out
}

// maskCommandForRisk 把引号字符串、反引号替换、shell 注释与 heredoc 正文
// 按字节原地改成空格（保留换行与长度），使危险模式只在真实命令位置被匹配。
// 引号状态跨行保持（与 shell 语义一致）；>= 0x80 的 UTF-8 字节永不参与匹配，
// 因此 CJK 文本不会被拆分或误报。
func maskCommandForRisk(cmd string) string {
	out := []byte(cmd)
	maskLine := func(from, to int) {
		for k := from; k < to && k < len(out); k++ {
			if out[k] != '\n' {
				out[k] = ' '
			}
		}
	}

	inSingle, inDouble, inBacktick := false, false, false
	heredoc := ""
	lineStart := 0

	for pos := 0; pos < len(cmd); {
		lineEnd := len(cmd)
		if rel := strings.IndexByte(cmd[pos:], '\n'); rel >= 0 {
			lineEnd = pos + rel
		}
		line := cmd[pos:lineEnd]

		if heredoc != "" {
			// heredoc 正文与其结束分隔符整行 mask。
			if strings.TrimSpace(line) == heredoc {
				heredoc = ""
			}
			maskLine(pos, lineEnd)
		} else {
			for k := 0; k < len(line); {
				idx := pos + k
				c := line[k]

				if inSingle {
					if c == '\'' {
						inSingle = false
					}
					out[idx] = ' '
					k++
					continue
				}
				if inDouble {
					if c == '\\' && k+1 < len(line) {
						out[idx], out[idx+1] = ' ', ' '
						k += 2
						continue
					}
					if c == '"' {
						inDouble = false
					}
					out[idx] = ' '
					k++
					continue
				}
				if inBacktick {
					if c == '\\' && k+1 < len(line) {
						out[idx], out[idx+1] = ' ', ' '
						k += 2
						continue
					}
					if c == '`' {
						inBacktick = false
					}
					out[idx] = ' '
					k++
					continue
				}

				switch c {
				case '\\':
					if k+1 < len(line) {
						out[idx], out[idx+1] = ' ', ' '
						k += 2
					} else {
						out[idx] = ' '
						k++
					}
					continue
				case '\'':
					inSingle = true
					out[idx] = ' '
					k++
					continue
				case '"':
					inDouble = true
					out[idx] = ' '
					k++
					continue
				case '`':
					inBacktick = true
					out[idx] = ' '
					k++
					continue
				case '#':
					// 词首的 '#' 开启注释，吞到行尾；词中 '#' 按字面保留。
					if idx == lineStart || idx > 0 && isRiskWordSep(cmd[idx-1]) {
						maskLine(idx, lineEnd)
						k = len(line)
						continue
					}
				}

				// heredoc 起始标记 <<EOF / <<-EOF / <<'EOF'，连同本行余下部分 mask。
				if c == '<' && k+1 < len(line) && line[k+1] == '<' {
					j := k + 2
					if j < len(line) && line[j] == '-' {
						j++
					}
					for j < len(line) && (line[j] == '\'' || line[j] == '"') {
						j++
					}
					dstart := j
					for j < len(line) && isRiskDelimByte(line[j]) {
						j++
					}
					if j > dstart {
						heredoc = line[dstart:j]
					}
					maskLine(idx, lineEnd)
					break
				}
				k++
			}
		}

		if lineEnd == len(cmd) {
			break
		}
		pos = lineEnd + 1
		lineStart = pos
	}
	return string(out)
}

// isRiskWordSep 报告 c 是否为 shell 单词分隔符或命令分隔符。
func isRiskWordSep(c byte) bool {
	switch c {
	case ' ', '\t', '\r', '|', ';', '&', '(', ')', '<', '>':
		return true
	}
	return false
}

// isRiskDelimByte 报告 c 能否作为 heredoc 分隔符的一部分。
func isRiskDelimByte(c byte) bool {
	return isAlnumByte(c) || c == '_' || c == '-' || c == '.'
}

// isAlnumByte 报告 c 是否为 ASCII 字母或数字。
func isAlnumByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}
