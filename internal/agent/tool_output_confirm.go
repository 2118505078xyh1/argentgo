package agent

import (
	"context"
	"strings"
	"sync/atomic"

	"example.com/argentgo/internal/llm"
)

// 本文件实现"命令输出进入对话前的二次确认"判定能力，用于防数据外泄。
// 判定本身是纯函数：不发出事件、不阻塞、不修改任何状态；真正的确认动作
// （可能阻塞等待 operator）由 confirmToolOutputBeforeAppend 在显式 opt-in 后完成。

// toolOutputConfirmMinBytes 是触发二次确认的最小输出字节数。
// 它是固定常量，因而阈值有界：低于该规模的输出即使来自敏感命令，也不足以造成批量外泄。
const toolOutputConfirmMinBytes = 4096

// sensitiveOutputCommands 是"本身就会倾倒环境或文件内容"的命令/工具名集合。
// 命中这些名字意味着输出极可能包含凭据或密钥。这里刻意保持窄集合，
// 避免把普通只读命令误判为敏感；通用 shell（如 bash）由输出内容标记兜底。
var sensitiveOutputCommands = map[string]bool{
	"env":      true,
	"printenv": true,
	"set":      true,
	"export":   true,
	"cat":      true,
	"type":     true,
	"history":  true,
}

// sensitiveOutputMarkers 是输出内容中出现即说明可能含有敏感凭据的文本片段。
// 它们统一为小写，匹配时先把输出整体转为小写，因此大小写不再敏感。
// 匹配按字节进行，对 UTF-8/CJK 内容天然安全（不会切断多字节字符）。
var sensitiveOutputMarkers = []string{
	"begin private key",
	"begin rsa private key",
	"begin openssh private key",
	"begin ec private key",
	"aws_secret_access_key",
	"aws_access_key_id",
	"secret_access_key",
	"api_key",
	"apikey",
	"secret_key",
	"private_key",
	"access_token",
	"client_secret",
	"password=",
	"passwd=",
	"token=",
	"secret=",
}

// toolOutputConfirm 控制是否把二次确认接线到派发路径，默认关闭。
// 关闭（默认）时输出逐字节保持原样，既不阻塞也不发出任何事件；
// 打开后，仅当 shouldConfirmToolOutput 判定需要确认时才调用审批器，
// 因此禁止外泄的语义变更不会被悄悄引入既有运行。这是进程级开关，由 app 层启动时设置。
var toolOutputConfirm atomic.Bool

// SetToolOutputConfirmation 打开或关闭"命令输出写回对话前的二次确认"。
// 关闭（默认）与引入该能力之前逐字节一致；打开后确认会等待 operator 答复，可能阻塞，
// 因此只应在需要防数据外泄的场景显式启用。
func SetToolOutputConfirmation(enabled bool) {
	toolOutputConfirm.Store(enabled)
}

// toolOutputConfirmationEnabled 报告当前是否启用了输出二次确认。
func toolOutputConfirmationEnabled() bool {
	return toolOutputConfirm.Load()
}

// isSensitiveOutputCommand 报告 toolName 是否属于倾倒环境/文件内容的高危命令。
// 名字先去掉首尾空白并转小写，使 "ENV"、" cat " 等写法也无法绕过判定。
func isSensitiveOutputCommand(toolName string) bool {
	return sensitiveOutputCommands[strings.ToLower(strings.TrimSpace(toolName))]
}

// hasSensitiveOutputMarker 报告输出内容是否含有敏感凭据标记。
// 先把输出转小写再按字节匹配，大小写不敏感且对 UTF-8/CJK 内容安全。
func hasSensitiveOutputMarker(output string) bool {
	if len(output) == 0 {
		return false
	}
	lower := strings.ToLower(output)
	for _, marker := range sensitiveOutputMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// shouldConfirmToolOutput 报告一次工具输出在写回对话之前是否需要 operator 二次确认。
// 它只做判定，不发出事件、不阻塞、不修改任何状态；确认动作由调用方完成。
//
// 仅当"命令/工具名敏感"或"输出内容含敏感凭据标记"，且输出规模达到阈值时返回 true。
// bytes 由调用方给出（通常为 len(output)），使调用方无需复制内容即可传递原始字节数；
// 传 0 或负数时回退为 len(output)。阈值是固定常量，因而是有界的；比较基于字节数，
// 对包含 CJK 的 UTF-8 文本不会误判或越界。
func shouldConfirmToolOutput(toolName, output string, bytes int) bool {
	n := bytes
	if n <= 0 {
		n = len(output)
	}
	if n < toolOutputConfirmMinBytes {
		return false
	}
	return isSensitiveOutputCommand(toolName) || hasSensitiveOutputMarker(output)
}

// toolOutputWithheldReason 是 operator 拒绝或无法完成二次确认时写回对话的占位文本。
// 它刻意不含任何原始输出，确保敏感数据不会通过记录进入模型可见的对话。
const toolOutputWithheldReason = "error: tool output withheld pending operator confirmation (sensitive command produced a large output)"

// toolOutputConfirmApprovalName 是二次确认在审批事件中的工具名前缀，
// 使 operator 能把它与普通工具审批区分开。
const toolOutputConfirmApprovalName = "confirm-output"

// confirmToolOutputBeforeAppend 在把工具输出写回对话之前做可选的二次确认。
// 它必须在循环 goroutine 上、与 finishCall/appendTool 相邻地调用。
//
// 默认关闭或判定无需确认时原样返回输出，行为与既有路径逐字节一致。
// 仅当显式 opt-in 且 shouldConfirmToolOutput 判定需要确认时才通过 Approve 询问 operator：
// 拒绝、出错或未配置审批器时返回不含原始输出的占位文本（fail-closed），
// 使敏感数据不会进入对话。该路径会阻塞，故默认关闭。
func (s *runState) confirmToolOutputBeforeAppend(ctx context.Context, call llm.ToolCall, output string) string {
	if !toolOutputConfirmationEnabled() || ctx.Err() != nil {
		return output
	}
	if !shouldConfirmToolOutput(call.Function.Name, output, len(output)) {
		return output
	}
	// 没有审批器时 fail-closed：宁可扣住输出，也不让敏感数据进入对话。
	if s.opts.Approve == nil {
		return toolOutputWithheldReason
	}
	// 摘要针对输出本身而非参数：operator 确认的对象正是这段即将外泄的内容。
	digest := approvalDigest(output)
	s.emit(ApprovalRequestedEvent(toolOutputConfirmApprovalName, digest, call.ID))
	allowed, err := s.opts.Approve.Ask(ctx, toolOutputConfirmApprovalName+":"+call.Function.Name, digest)
	s.emit(ApprovalResolvedEvent(toolOutputConfirmApprovalName, call.ID, err == nil && allowed))
	if err != nil || !allowed {
		return toolOutputWithheldReason
	}
	return output
}
