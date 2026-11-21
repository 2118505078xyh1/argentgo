package security

import "strings"

// 危险原因常量。它们是稳定的、面向用户的中文文本，供 R14 ApproveDanger
// 接线在审批提示中展示。保持为包内常量，确保同一危险类别永远返回相同文本。
const (
	dangerReasonDelete       = "删除文件"
	dangerReasonMove         = "移动文件"
	dangerReasonWrite        = "写入文件"
	dangerReasonDownloadExec = "网络下载执行"
	dangerReasonReverseShell = "反弹 shell"
	dangerReasonEncodedExec  = "编码执行"
	dangerReasonMutate       = "修改文件"
)

// DangerReason 返回 command 被判定为危险的原因文本；没有危险时返回空串。
//
// 它是纯函数：复用同包的 DetectThreat 与 FindFileEditingCommand 判定，不新增
// 任何威胁模式，也不修改入参、不保留全局状态。结果仅由输入决定，同一输入
// 永远得到同一文本。底层判定按字节扫描，ASCII 与 CJK 命令均被安全处理。
func DangerReason(command string) string {
	// 威胁分类器优先：curl|bash、反弹 shell、编码执行与强制删除等模式有稳定的
	// 类别，可直接映射到原因，无需依赖 FindFileEditingCommand 返回的证据片段。
	if m := DetectThreat(command); m.Detected() {
		switch m.Category {
		case ThreatForcedRM:
			return dangerReasonDelete
		case ThreatDownloadExec:
			return dangerReasonDownloadExec
		case ThreatReverseShell:
			return dangerReasonReverseShell
		case ThreatEncodedExec:
			return dangerReasonEncodedExec
		case ThreatWrapper:
			return dangerReasonMutate
		}
	}

	// 其余文件编辑类危险由 FindFileEditingCommand 给出 token（如 "rm"、
	// ">/etc/passwd"、"git push"），据此归类到稳定的原因文本。
	token := FindFileEditingCommand(command)
	if token == "" {
		return ""
	}
	return dangerReasonForToken(token)
}

// IsDangerous 报告 command 是否被判定为危险。
func IsDangerous(command string) bool {
	return DangerReason(command) != ""
}

// 以下几组命令词把 FindFileEditingCommand 返回的修改类 token 归到稳定的原因。
// 词表刻意保守：只列常见的 POSIX / cmd.exe / PowerShell 命令；未能识别的命令
// 回退到 dangerReasonMutate，避免漏报。
var (
	deleteCommands = map[string]bool{
		"rm": true, "rmdir": true, "unlink": true, "shred": true,
		"del": true, "erase": true, "remove-item": true, "ri": true,
		"remove-itemproperty": true,
	}
	moveCommands = map[string]bool{
		"mv": true, "move": true, "ren": true, "rename": true,
		"move-item": true, "rename-item": true, "rni": true,
	}
	writeCommands = map[string]bool{
		"cp": true, "copy": true, "xcopy": true, "robocopy": true,
		"sed": true, "tee": true, "dd": true, "truncate": true,
		"touch": true, "split": true, "patch": true, "install": true,
		"ln": true, "mkdir": true, "md": true,
		"set-content": true, "out-file": true, "add-content": true,
		"clear-content": true, "clc": true, "copy-item": true,
		"new-item": true, "ni": true, "set-item": true, "si": true,
		"set-itemproperty": true, "new-itemproperty": true,
		"compress-archive": true, "expand-archive": true,
	}
	// 这些工具由第二个 token 决定是否为修改操作；FindFileEditingCommand 返回的
	// token 形如 "git push"、"npm install"，它们一律归到 dangerReasonMutate。
	subcommandTools = map[string]bool{
		"git": true, "go": true, "npm": true,
		"pip": true, "pip3": true, "cargo": true,
	}
)

// dangerReasonForToken 把 FindFileEditingCommand 返回的违规 token 映射为原因。
func dangerReasonForToken(token string) string {
	fields := strings.Fields(token)
	if len(fields) == 0 {
		return ""
	}
	cmd := strings.ToLower(fields[0])

	// FindFileEditingCommand 对重定向返回 ">/target" 之类的 token。
	if strings.HasPrefix(cmd, ">") {
		return dangerReasonWrite
	}
	// 按第二个 token 判定的工具：只要 token 被返回，子命令就是修改类的。
	if subcommandTools[cmd] {
		return dangerReasonMutate
	}

	switch {
	case deleteCommands[cmd]:
		return dangerReasonDelete
	case moveCommands[cmd]:
		return dangerReasonMove
	case writeCommands[cmd]:
		return dangerReasonWrite
	default:
		return dangerReasonMutate
	}
}
