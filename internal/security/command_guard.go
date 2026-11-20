package security

import (
	"encoding/json"
	"log"
	"os"
	"path"
	"strings"
	"unicode"
)

// 护栏运行时模式，由 ARGENT_GUARDRAILS 选择。
const (
	guardrailShadow  = "shadow"
	guardrailEnforce = "enforce"
	guardrailOff     = "off"

	// defaultGuardrailMode 刻意设为 "enforce"：argentgo 是个人本地工具，
	// 必须像该开关出现之前一样继续拦截修改类命令。想先测量误报的操作员可以
	// 显式选择 "shadow"。
	defaultGuardrailMode = guardrailEnforce
)

// GuardrailMode 从 ARGENT_GUARDRAILS 返回运行时护栏模式。
// 值不区分大小写，并忽略周围的空白。未设置、空值或无法识别的值都会
// 回退到 "enforce"，从而保留默认安全行为（检测到的修改类命令会被拦截）。
func GuardrailMode() string {
	return normalizeGuardrailMode(os.Getenv("ARGENT_GUARDRAILS"))
}

func normalizeGuardrailMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case guardrailShadow:
		return guardrailShadow
	case guardrailOff:
		return guardrailOff
	case guardrailEnforce:
		return guardrailEnforce
	default:
		return defaultGuardrailMode
	}
}

// applyGuardrailMode 把检测到的修改类 token 转成调用方据以行动的决定。
// "enforce" 原样返回 token，让调用方拦截；"shadow" 记录本会被拦截的内容但
// 不返回 token；"off" 同样不返回 token（调用方会完全跳过检测）。
func applyGuardrailMode(token, mode string) string {
	if token == "" {
		return ""
	}
	switch mode {
	case guardrailOff:
		return ""
	case guardrailShadow:
		log.Printf("security: guardrails shadow mode: would block mutating command %q", token)
		return ""
	default: // guardrailEnforce
		return token
	}
}

// FindFileEditingCommand 返回 command 中的修改类 shell token，没找到则返回
// ""。它是一个感知 token 的分类器，思路借鉴 Cline 的命令守卫：在分词之前会
// 遮盖引号字符串、反引号替换、heredoc 正文和 shell 注释，因此字符串里的字面量
// "rm" 不会触发误报。唯一的例外是传给 `bash -c`/`sh -c`/`bash -lc` 的带引号
// 载荷：该字面量会从原始命令中恢复出来并递归分类，因此无法借包装器隐藏修改类
// 命令。
//
// 该函数是纯函数：没有副作用，也不保留全局状态。它刻意保守 —— 修改类命令可能
// 漏过，但读取类命令（ls、cat、echo、git status、go test、rg、find、type、
// Get-Content）绝不能被拦截。返回值是违规 token 在命令中的原样
// （例如 "rm" 或 ">/etc/passwd"）。
func FindFileEditingCommand(command string) string {
	return findFileEditingCommandDepth(command, 0)
}

// findFileEditingCommandDepth 是带递归预算的 FindFileEditingCommand，用于
// 从 shell -c 包装器中恢复的字面量载荷。每一步递归至少消耗包装器文本，因此
// 嵌套是有限的；上限使恶意的深层嵌套命令也能被低成本分类。
func findFileEditingCommandDepth(command string, depth int) string {
	if strings.TrimSpace(command) == "" {
		return ""
	}

	// POSIX 文件/shell 修改命令，加上 cmd.exe 和 PowerShell 的等价命令。
	// token 比较不区分大小写，因为 PowerShell cmdlet 和别名也不区分。
	mutating := map[string]bool{
		// POSIX.
		"rm": true, "rmdir": true, "mv": true, "cp": true, "sed": true,
		"tee": true, "dd": true, "chmod": true, "chown": true, "chgrp": true,
		"truncate": true, "touch": true, "mkdir": true, "install": true,
		"ln": true, "unlink": true, "shred": true, "split": true, "patch": true,
		// cmd.exe.
		"del": true, "erase": true, "move": true, "copy": true, "xcopy": true,
		"robocopy": true, "ren": true, "rename": true, "md": true, "rd": true,
		// PowerShell cmdlet 和常见别名。
		"remove-item": true, "set-content": true, "out-file": true,
		"add-content": true, "clear-content": true, "move-item": true,
		"copy-item": true, "new-item": true, "set-item": true,
		"rename-item": true, "remove-itemproperty": true, "set-itemproperty": true,
		"new-itemproperty": true, "compress-archive": true, "expand-archive": true,
		"ri": true, "ni": true, "si": true, "rni": true, "clc": true,
	}

	// 由第二个 token 决定调用是否产生修改的工具。只读子命令
	// （git status、go test、go vet、go list、cargo check、npm view、
	// pip freeze 等）刻意不列入。
	subcommands := map[string]map[string]bool{
		"git": {
			"push": true, "commit": true, "checkout": true, "reset": true,
			"clean": true, "rebase": true, "merge": true, "stash": true,
			"add": true, "rm": true, "mv": true, "restore": true, "revert": true,
			"cherry-pick": true, "apply": true, "am": true, "fetch": true, "pull": true,
		},
		"go": {
			"build": true, "install": true, "get": true, "mod": true,
			"fmt": true, "generate": true, "clean": true,
		},
		"npm": {
			"install": true, "i": true, "ci": true, "add": true, "remove": true,
			"rm": true, "uninstall": true, "update": true, "publish": true,
			"pack": true,
		},
		"pip":  {"install": true, "uninstall": true},
		"pip3": {"install": true, "uninstall": true},
		"cargo": {
			"build": true, "install": true, "add": true, "remove": true,
			"update": true, "publish": true, "fmt": true, "clean": true,
		},
	}

	cleaned := sanitizeCommand(command)
	// 包装程序（sudo、env、nohup、command、builtin、exec）不会改变被包装命令的
	// 行为。威胁分类器已经用 reduceWrappers（threat_patterns.go）剥离它们；
	// token 分类器也必须这样做，否则一个包装词就能把所有修改类命令对计划模式
	// 守卫和自动审批器隐藏起来。reduceWrappers 会保留前面的分隔符，因此归约后
	// 命令拆分仍然有效。
	reduced, _ := reduceWrappers(cleaned)
	for _, segment := range splitOnSeparators(reduced) {
		fields := strings.Fields(segment)
		if len(fields) == 0 {
			continue
		}
		// 包装器去掉后，`env FOO=1 rm file` 会在开头留下一个赋值。跳过开头的赋值
		// 找到可执行文件，然后像对待未包装的命令一样判断这个词。
		cmdAt := 0
		for cmdAt < len(fields) && isAssignmentWord(fields[cmdAt]) {
			cmdAt++
		}
		if cmdAt == len(fields) {
			continue
		}
		cmd := strings.ToLower(fields[cmdAt])
		if mutating[cmd] {
			return fields[cmdAt]
		}
		if subs, ok := subcommands[cmd]; ok && cmdAt+1 < len(fields) {
			sub := strings.ToLower(fields[cmdAt+1])
			if subs[sub] {
				return fields[cmdAt] + " " + fields[cmdAt+1]
			}
		}
		for i, field := range fields {
			// 一个 shell 词可以携带多个重定向（例如 `>/etc/passwd>/tmp/scratch`）。
			// 只检查最后一个 `>`，当最终目标是临时路径时就会把整个词加入白名单，
			// 尽管更早的 `>` 仍然会截断真实文件。检查词中的每个 `>`，
			// 确保没有更早的写入被隐藏。
			for off := 0; off < len(field); {
				rel := strings.IndexByte(field[off:], '>')
				if rel < 0 {
					break
				}
				idx := off + rel
				// `>>` 是追加重定向：跳过整个操作符序列。目标延伸到同一词中的下一个 `>`
				// （或到词尾）。
				start := idx + 1
				for start < len(field) && field[start] == '>' {
					start++
				}
				end := start
				for end < len(field) && field[end] != '>' {
					end++
				}
				target := ""
				if start < len(field) {
					target = field[start:end]
				} else if i+1 < len(fields) {
					target = fields[i+1]
				}
				off = end
				if target == "" || strings.HasPrefix(target, "&") {
					continue // 2>&1 之类的不是文件写入
				}
				lower := strings.ToLower(strings.Trim(target, "\"'"))
				if lower == "null" || lower == "/dev/null" || lower == "nul" || lower == "nul:" {
					continue
				}
				if !isTempPath(target) {
					return ">" + target
				}
			}
		}
	}

	// 附加威胁检测：不是文件编辑的危险命令（curl|bash、反弹 shell、编码执行、
	// 通过包装器发起的删除）会被报告，使该分类器的每个消费方都对其进行拦截。
	// 只返回 HIGH 置信度的威胁；较低置信度的匹配留作诊断。返回 Evidence 使
	// token 保持为命令的子串，保留分类器现有的长度约定。
	if m := DetectThreat(command); m.IsHigh() {
		return m.Evidence
	}

	// P0 修复：`bash -c`/`sh -c`/`bash -lc` 后面的带引号载荷会被 sanitizeCommand
	// 清空，从而对上面的分类器隐藏修改类 token（rm、mv、git push 等）。
	// 从原始命令中恢复字面量载荷并在其上运行整个分类器。提取只在真实命令位置
	// 触发且要求带引号载荷，因此只是在字符串中提到 "bash -c" 的良性命令
	// 不受影响。
	if depth < maxShellLiteralDepth {
		for _, payload := range shellLiteralPayloads(command) {
			if token := findFileEditingCommandDepth(payload, depth+1); token != "" {
				return token
			}
		}
	}
	return ""
}

// guardShellCommand 从 shell 工具的 JSON 参数中提取命令字符串，并应用运行时
// 护栏模式："enforce" 返回修改类 token 让调用方拦截，"shadow" 记录但不返回
// token，"off" 跳过检测。只有 bash 和 powershell 是 shell 工具；其他一律忽略。
// 格式错误的载荷不会产生匹配（漏报是安全的）。
func guardShellCommand(toolName string, args json.RawMessage) string {
	mode := GuardrailMode()
	if mode == guardrailOff {
		return "" // off：完全跳过检测，永不拦截
	}
	name := strings.ToLower(strings.TrimSpace(toolName))
	if name != "bash" && name != "powershell" {
		return ""
	}
	var payload struct {
		Command string `json:"command"`
		Script  string `json:"script"`
	}
	if err := json.Unmarshal(args, &payload); err != nil {
		return ""
	}
	command := payload.Command
	if strings.TrimSpace(command) == "" {
		command = payload.Script
	}

	// 这是拦截决策点。HIGH 置信度威胁（以及任何文件编辑 token）会被交给现有的
	// 三态护栏："enforce" 返回 token 让调用方拦截，"shadow" 记录但不返回任何
	// 东西，"off" 已在上面短路。威胁分类器也已并入 FindFileEditingCommand，
	// 因此这个兜底让决策保持显式，即使该分类器被重构也很稳健。
	token := FindFileEditingCommand(command)
	if token == "" {
		if m := DetectThreat(command); m.IsHigh() {
			token = m.Evidence
		}
	}
	return applyGuardrailMode(token, mode)
}

// sanitizeCommand 遮盖 heredoc 正文，然后逐行调用 sanitizeLine。
func sanitizeCommand(command string) string {
	lines := strings.Split(command, "\n")
	heredoc := ""
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if heredoc != "" {
			// 只包含分隔符的行表示 heredoc 结束。正文和分隔符行都会被替换为空白，
			// 这样其中的任何 token 都不会被误认为命令。
			if strings.TrimSpace(line) == heredoc {
				heredoc = ""
			}
			out = append(out, "")
			continue
		}
		clean, delim := sanitizeLine(line)
		heredoc = delim
		out = append(out, clean)
	}
	return strings.Join(out, "\n")
}

// sanitizeLine 把引号字符串、反引号替换、shell 注释以及 heredoc 起始标记
// 置为空白。未被忽略的字符（包括 shell 分隔符）会保留，因此调用方仍可以把
// 命令拆开。
func sanitizeLine(line string) (string, string) {
	var b strings.Builder
	var heredoc string
	inSingle, inDouble, inBacktick := false, false, false
	escaped := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		if escaped {
			b.WriteByte(' ')
			escaped = false
			continue
		}
		if c == '\\' && !inSingle {
			escaped = true
			b.WriteByte(' ')
			continue
		}
		if inSingle {
			if c == '\'' {
				inSingle = false
			}
			b.WriteByte(' ')
			continue
		}
		if inDouble {
			if c == '"' {
				inDouble = false
			}
			b.WriteByte(' ')
			continue
		}
		if inBacktick {
			if c == '`' {
				inBacktick = false
			}
			b.WriteByte(' ')
			continue
		}
		switch c {
		case '\'':
			inSingle = true
			b.WriteByte(' ')
			continue
		case '"':
			inDouble = true
			b.WriteByte(' ')
			continue
		case '`':
			inBacktick = true
			b.WriteByte(' ')
			continue
		case '#':
			if i == 0 || isSeparatorByte(line[i-1]) {
				for ; i < len(line); i++ {
					b.WriteByte(' ')
				}
				return b.String(), heredoc
			}
		}
		if c == '<' && i+1 < len(line) && line[i+1] == '<' {
			j := i + 2
			if j < len(line) && line[j] == '-' {
				j++
			}
			for j < len(line) && (line[j] == '\'' || line[j] == '"') {
				j++
			}
			start := j
			for j < len(line) && isDelimiterByte(line[j]) {
				j++
			}
			if j > start {
				heredoc = line[start:j]
			}
			for k := i; k < j; k++ {
				b.WriteByte(' ')
			}
			i = j - 1
			continue
		}
		b.WriteByte(c)
	}
	return b.String(), heredoc
}

// splitOnSeparators 把一个 shell 程序拆成单条命令。分隔符本身会被丢弃；
// `;`、`&&`、`||`、`|`、`&`、括号和换行都会结束一条命令。重定向 `>` 不是分隔符。
func splitOnSeparators(s string) []string {
	out := []string{}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ';', '&', '|', '(', ')', '\n', '\r':
			out = append(out, b.String())
			b.Reset()
		default:
			b.WriteByte(s[i])
		}
	}
	return append(out, b.String())
}

// isAssignmentWord 报告 word 是否是命令开头由 `env` 和 POSIX shell 接受的
// VAR=value 赋值。只认纯 ASCII 标识符名，因此含 '=' 的普通参数不会被误认为
// 赋值而跳过。
func isAssignmentWord(word string) bool {
	eq := strings.IndexByte(word, '=')
	if eq <= 0 {
		return false
	}
	for i := 0; i < eq; i++ {
		c := word[i]
		if !isAlnum(c) && c != '_' {
			return false
		}
	}
	return true
}

func isDelimiterByte(b byte) bool {
	return b == '_' || b == '-' || b == '.' ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9')
}

func isSeparatorByte(b byte) bool {
	switch b {
	case ';', '&', '|', '(', ')', '<', '>', '\t', '\r':
		return true
	}
	return unicode.IsSpace(rune(b))
}

// isTempPath 报告 path 是否指向一个对此守卫而言写入无害的临时目录。
// os.TempDir 是读取环境变量，不是副作用，因此分类器保持无全局状态。
func isTempPath(candidate string) bool {
	p := strings.ReplaceAll(strings.Trim(strings.TrimSpace(candidate), "\"'"), "\\", "/")
	// 在前缀检查前先折叠 "." 和 ".."。目标可能只是*写法上*指向临时目录 ——
	// "/tmp/../etc/passwd" 或 "%TEMP%/../../evil" —— 但实际写到外面；
	// 裸前缀检查会错误地将其加入白名单，让重定向守卫完全漏掉修改类命令。
	p = path.Clean(p)
	lower := strings.ToLower(p)
	roots := []string{
		strings.ToLower(strings.ReplaceAll(os.TempDir(), "\\", "/")),
		"/tmp", "/var/tmp", "%temp%", "c:/windows/temp",
	}
	for _, root := range roots {
		root = path.Clean(strings.ToLower(root))
		if root == "" || root == "." {
			continue
		}
		if lower == root || strings.HasPrefix(lower, root+"/") {
			return true
		}
	}
	return false
}
