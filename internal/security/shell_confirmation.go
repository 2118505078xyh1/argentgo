package security

import (
	"fmt"
	"regexp"
	"strings"
)

// 本文件提供"危险 shell 命令需要操作员显式确认"这一能力的纯函数实现，
// 对应 aider 安全审计中的 explicit_yes_required：yolo/auto 模式不得静默放行
// 破坏性、提权或下载后执行的命令。函数只做判定并给出中文理由，不接触 stdin、
// 环境或任何全局状态，调用方（审批链）自行决定如何强制要求显式 yes。
// 已有的沙箱违规、环境脱敏与提示注入修复均不改动。

const (
	// confirmationReasonLimit 是理由的最大 rune 数。理由用于提示与审计，
	// 必须设上界，避免超长命令把提示撑爆。
	confirmationReasonLimit = 96
	// confirmationEvidenceRunes 是嵌入理由中的违规证据的最大 rune 数。
	confirmationEvidenceRunes = 24
)

// powershellCommandPayloadRe 在命令位置匹配 PowerShell 的 -Command/-c 带引号
// 载荷。标准库的 shellLiteralPayloads 只识别 bash/sh 家族，所以这里单独覆盖
// PowerShell，避免 `powershell -Command "Start-Process -Verb RunAs"` 隐藏提权。
var powershellCommandPayloadRe = regexp.MustCompile(`(?i)\b(?:powershell|pwsh)(?:\.exe)?\s+(?:-Command|-c)\s+("([^"]*)"|'([^']*)')`)

// privilegeCommands 是需要提权的命令 token。token 比较不区分大小写，
// 因为 PowerShell 别名与 cmdlet 也不区分大小写。它们与命令守卫的 token
// 分类器共用清洗逻辑，因此字符串字面量与注释里的同名文本不会误报。
var privilegeCommands = map[string]bool{
	// POSIX.
	"sudo": true, "su": true, "doas": true, "pkexec": true,
	// cmd.exe 与 PowerShell。
	"runas": true,
}

// ShellConfirmationRequired 是 shellConfirmationRequired 的导出入口，供安全链在
// 显式 opt-in 开启时判断一条 shell 命令是否需要操作员显式确认。判定逻辑与包内
// 调用完全一致：纯函数，返回 (是否需要确认, 中文理由)，CJK 文本按 rune 截断。
func ShellConfirmationRequired(command string) (bool, string) {
	return shellConfirmationRequired(command)
}

// shellConfirmationRequired 判断一条 shell 命令是否需要操作员的显式确认
// （即不允许被 yolo/auto 模式静默批准）。需要确认时返回 true 并给出一条
// 简短的中文理由；普通只读命令返回 (false, "")。
//
// 覆盖 aider 关注的三类危险命令：
//   - 破坏性/修改类命令（rm、重定向写文件、git push 等），复用命令守卫的
//     FindFileEditingCommand；
//   - 网络下载后直接执行、反弹 shell、编码后执行与强制删除，复用威胁分类器
//     DetectThreat；
//   - 提权命令（sudo、su、runas、Start-Process -Verb RunAs 等）。
//
// 返回理由经过长度限制且在 rune 边界截断，CJK 文本不会被切开。
// 该函数是纯函数：无副作用，不保留全局状态。
func shellConfirmationRequired(command string) (bool, string) {
	if strings.TrimSpace(command) == "" {
		return false, ""
	}

	// 网络下载执行、反弹 shell、编码执行等 HIGH 置信度威胁。必须先于命令守卫
	// 判断：FindFileEditingCommand 内部也会调用 DetectThreat，若先调用它，
	// 这些威胁会被统一归类为"破坏性"而丢失具体理由。只取 HIGH 置信度，
	// 与命令守卫的语义保持一致；中置信度的强制删除仍由下面的 token 分类器
	// 以"破坏性"归类。
	if threat := DetectThreat(command); threat.IsHigh() {
		return true, confirmationReason(threatConfirmReason(threat), threat.Evidence)
	}

	// 破坏性/修改类命令：命令守卫已经做了感知 token 的分类，包括引号、
	// heredoc、注释遮盖与 shell -c 包装器恢复。
	if token := FindFileEditingCommand(command); token != "" {
		return true, confirmationReason("检测到破坏性或写文件命令", token)
	}

	// 提权命令。
	if evidence := privilegeEscalationEvidence(command, 0); evidence != "" {
		return true, confirmationReason("检测到提权命令", evidence)
	}

	return false, ""
}

// threatConfirmReason 把威胁类别映射为稳定的中文理由前缀。
func threatConfirmReason(threat ThreatMatch) string {
	switch threat.Category {
	case ThreatDownloadExec:
		return "检测到网络下载后直接执行"
	case ThreatReverseShell:
		return "检测到反弹 shell"
	case ThreatEncodedExec:
		return "检测到编码后执行"
	case ThreatForcedRM:
		return "检测到强制删除"
	case ThreatWrapper:
		return "检测到危险命令包装"
	default:
		return "检测到危险命令"
	}
}

// privilegeEscalationEvidence 返回 command 中提权命令的证据 token；没有则
// 返回 ""。它与命令守卫共用 token 清洗和拆分逻辑，因此字符串字面量里的
// "sudo" 或注释不会被误报。返回的证据保持命令中的原样写法。
// depth 是恢复 shell -c 带引号载荷时的递归预算，防止深层嵌套输入拖慢分类。
func privilegeEscalationEvidence(command string, depth int) string {
	if strings.TrimSpace(command) == "" {
		return ""
	}

	cleaned := sanitizeCommand(command)
	for _, segment := range splitOnSeparators(cleaned) {
		fields := strings.Fields(segment)
		if len(fields) == 0 {
			continue
		}
		// 跳过开头的 VAR=value 赋值（env 与 POSIX shell 都接受），
		// 与命令守卫保持一致：`env FOO=1 sudo x` 仍应被识别。
		cmdAt := 0
		for cmdAt < len(fields) && isAssignmentWord(fields[cmdAt]) {
			cmdAt++
		}
		if cmdAt == len(fields) {
			continue
		}
		lower := strings.ToLower(fields[cmdAt])
		if privilegeCommands[lower] {
			return fields[cmdAt]
		}
		// PowerShell 的 Start-Process -Verb RunAs 形式：真正的可执行文件
		// 未提权，但 Verb 参数把它提升为管理员。
		if lower == "start-process" || lower == "start-process.exe" {
			for _, field := range fields[cmdAt+1:] {
				if strings.EqualFold(strings.Trim(field, "\"'"), "runas") {
					return "Start-Process -Verb RunAs"
				}
			}
		}
	}

	// 带引号的 -c/-Command 载荷已被清洗器置空；从原始命令中恢复后递归分类，
	// 避免 `powershell -Command "Start-Process -Verb RunAs"` 之类的隐藏。
	if depth < maxShellLiteralDepth {
		for _, payload := range append(shellLiteralPayloads(command), powershellCommandPayloads(command)...) {
			if evidence := privilegeEscalationEvidence(payload, depth+1); evidence != "" {
				return evidence
			}
		}
	}
	return ""
}

// powershellCommandPayloads 返回每次 PowerShell -Command/-c 调用的带引号
// 命令载荷；没有则返回 nil。它只负责提取，不判定危险性。
func powershellCommandPayloads(command string) []string {
	var out []string
	for _, match := range powershellCommandPayloadRe.FindAllStringSubmatch(command, -1) {
		payload := match[2]
		if payload == "" {
			payload = match[3]
		}
		if strings.TrimSpace(payload) != "" {
			out = append(out, payload)
		}
	}
	return out
}

// confirmationReason 组装一条有界的中文理由。证据会先经过控制字符剥离、
// 去空白和 rune 上限截断，最终整条理由再按 confirmationReasonLimit 截断，
// 保证 CJK 文本不会在 rune 中间被切开。
func confirmationReason(prefix, evidence string) string {
	reason := prefix
	if bounded := boundedConfirmationEvidence(evidence); bounded != "" {
		reason += fmt.Sprintf("（%s）", bounded)
	}
	reason += "，需要显式确认 yes"
	return runeHead(reason, confirmationReasonLimit)
}

// boundedConfirmationEvidence 把违规证据压到适合放进理由的长度。
func boundedConfirmationEvidence(evidence string) string {
	text := strings.TrimSpace(stripANSIAndControls(evidence))
	if text == "" {
		return ""
	}
	return runeHead(text, confirmationEvidenceRunes)
}
