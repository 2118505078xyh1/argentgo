package security

import (
	"strings"
)

// 本文件提供危险命令的**细分归因**（学自 codex shell-command
// is_dangerous_command.rs）。它不替代 ClassifyCommand 的风险分级，而是在审批 UX
// 层给出"为什么危险"的具体理由（如"强制删除 rm -f"），让用户能据此决策。
//
// 纯函数、无副作用、无环境/时钟依赖。判定保守：宁可少报具体理由（回退到通用
// destructive 文案），也不误报导致用户放松警惕。

// DangerousCommandKind 是细分种类。
type DangerousCommandKind string

const (
	DangerForcedRm     DangerousCommandKind = "forced-rm"     // rm -f / --force
	DangerRecursiveRm  DangerousCommandKind = "recursive-rm"  // rm -r / -rf
	DangerInterpreter  DangerousCommandKind = "interpreter"   // python -c / node -e 等任意代码执行
	DangerShellWrapper DangerousCommandKind = "shell-wrapper" // bash -c / sh -lc / cmd /c
	DangerPrivilege    DangerousCommandKind = "privilege"     // sudo / runas
	DangerDiskWipe     DangerousCommandKind = "disk-wipe"     // mkfs / dd / format
	DangerEncodedExec  DangerousCommandKind = "encoded-exec"  // powershell -EncodedCommand
)

// DangerousCommandMatch 是一次细分命中。
type DangerousCommandMatch struct {
	Kind     DangerousCommandKind
	Token    string // 触发的规范化 token（如 "rm"）
	Platform string // "posix" | "windows" | ""
}

// maxWrapperDepth 是包装器递归归约的显式深度上限（对齐 codex 的 8）。
// 超过上限时 classifyCommand 的 reduceWrappers 应 fail-closed 为 forbidden，
// 避免栈溢出或被超长包装链绕过分类。
const maxWrapperDepth = 8

// DangerousCommandDetail 返回命令中所有可识别的危险细分；没有具体细分时返回
// 空切片（调用方仍可用 ClassifyCommand 的通用风险等级）。
func DangerousCommandDetail(command string) []DangerousCommandMatch {
	cleaned := sanitizeCommand(command)
	tokens := tokenizeForDetail(cleaned)
	var out []DangerousCommandMatch
	seen := make(map[DangerousCommandKind]bool)

	add := func(kind DangerousCommandKind, token, platform string) {
		if !seen[kind] {
			seen[kind] = true
			out = append(out, DangerousCommandMatch{Kind: kind, Token: token, Platform: platform})
		}
	}

	for i, tok := range tokens {
		name := normalizeCommandName(tok)
		switch name {
		case "rm", "unlink", "shred", "del", "remove-item", "ri":
			flags := tokens[i+1:]
			if hasForceFlag(flags) {
				add(DangerForcedRm, name, platformFor(name))
			}
			if hasRecursiveFlag(flags) {
				add(DangerRecursiveRm, name, platformFor(name))
			}
		case "python", "python3", "node", "ruby", "perl", "php":
			if hasInlineCodeFlag(tokens[i+1:]) {
				add(DangerInterpreter, name, "posix")
			}
		case "bash", "sh", "zsh", "dash":
			if hasShellCommandFlag(tokens[i+1:]) {
				add(DangerShellWrapper, name, "posix")
			}
		case "cmd":
			if hasShellCommandFlag(tokens[i+1:]) {
				add(DangerShellWrapper, name, "windows")
			}
		case "powershell", "pwsh":
			if hasEncodedFlag(tokens[i+1:]) {
				add(DangerEncodedExec, name, "windows")
			}
		case "sudo", "runas", "doas":
			add(DangerPrivilege, name, platformFor(name))
		case "mkfs", "dd", "format":
			add(DangerDiskWipe, name, platformFor(name))
		}
	}
	return out
}

// DetailSummary 返回给用户看的中文短句（每条一行，确定性顺序）。
func DetailSummary(matches []DangerousCommandMatch) string {
	var b strings.Builder
	for _, m := range matches {
		if b.Len() > 0 {
			b.WriteString("；")
		}
		switch m.Kind {
		case DangerForcedRm:
			b.WriteString("强制删除（" + m.Token + " 带 force 标志，无法恢复）")
		case DangerRecursiveRm:
			b.WriteString("递归删除（" + m.Token + " 带 recursive 标志）")
		case DangerInterpreter:
			b.WriteString("解释器内联执行任意代码（" + m.Token + "）")
		case DangerShellWrapper:
			b.WriteString("Shell 包装执行任意命令（" + m.Token + "）")
		case DangerPrivilege:
			b.WriteString("权限提升（" + m.Token + "）")
		case DangerDiskWipe:
			b.WriteString("磁盘/数据破坏（" + m.Token + "）")
		case DangerEncodedExec:
			b.WriteString("编码命令执行（" + m.Token + "）")
		}
	}
	return b.String()
}

func platformFor(name string) string {
	switch name {
	case "del", "remove-item", "ri", "cmd", "powershell", "pwsh", "runas", "format":
		return "windows"
	default:
		return "posix"
	}
}

// tokenizeForDetail 做轻量分词：按空白与常见 shell 分隔符切，去引号。
func tokenizeForDetail(s string) []string {
	fields := strings.Fields(strings.NewReplacer(
		"&&", " ", "||", " ", ";", " ", "|", " ",
	).Replace(s))
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.Trim(f, `"'`)
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

func hasForceFlag(args []string) bool {
	for _, a := range args {
		if a == "-f" || a == "--force" || a == "-F" || a == "/F" || strings.Contains(strings.ToLower(a), "force") {
			return true
		}
		// 组合短标志：-rf / -fr / -rff 等同时含 r 与 f。
		if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") {
			hasR, hasF := false, false
			for _, ch := range a[1:] {
				if ch == 'r' || ch == 'R' {
					hasR = true
				}
				if ch == 'f' || ch == 'F' {
					hasF = true
				}
			}
			if hasR && hasF {
				return true
			}
		}
	}
	return false
}

func hasRecursiveFlag(args []string) bool {
	for _, a := range args {
		if a == "-r" || a == "-R" || a == "-rf" || a == "-fr" || a == "--recursive" {
			return true
		}
	}
	return false
}

func hasInlineCodeFlag(args []string) bool {
	for _, a := range args {
		if a == "-c" || a == "-e" || a == "--eval" || a == "-Command" {
			return true
		}
	}
	return false
}

func hasShellCommandFlag(args []string) bool {
	for _, a := range args {
		if a == "-c" || a == "-lc" || a == "/c" || a == "-Command" {
			return true
		}
	}
	return false
}

func hasEncodedFlag(args []string) bool {
	for _, a := range args {
		if strings.EqualFold(a, "-EncodedCommand") || strings.EqualFold(a, "-enc") {
			return true
		}
	}
	return false
}
