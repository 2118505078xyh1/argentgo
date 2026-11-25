package app

import (
	"context"
	"fmt"
	"path/filepath"

	"example.com/argentgo/internal/security"
	"example.com/argentgo/internal/session"
)

// undoApprover 代表已由用户显式触发的撤销动作。撤销只由 TUI 快捷键或 CLI
// 子命令发起，按键/命令本身就是一次明确的人工确认，因此审批阶段直接放行；
// 策略与审计仍照常执行，不会因为有审批器就跳过任何硬关卡。
type undoApprover struct{}

func (undoApprover) Ask(ctx context.Context, name, args string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return true, nil
}

// undoChainFor 为一次 checkpoint 恢复构造与正常运行同构的
// 策略 → 审批 → 审计链。审计落在与工具调用相同的目录下。
func undoChainFor(res Resolved, sessionID string) *security.Chain {
	return security.NewChain(policyFor(res), undoApprover{},
		security.NewAuditLog(filepath.Join(res.Storage, "audit")), sessionID)
}

// checkpointPaths 提取撤销清单里的相对路径，供安全链的 CheckRestore 审核。
func checkpointPaths(cp session.Checkpoint) []string {
	paths := make([]string, 0, len(cp.Files))
	for _, file := range cp.Files {
		paths = append(paths, file.Path)
	}
	return paths
}

// checkUndoRestore 在真正还原文件之前把清单交给安全链。拒绝时返回带上下文的
// 可读错误，调用方据此放弃撤销；成功时返回 nil，工作区字节尚未被触碰。
func checkUndoRestore(ctx context.Context, chain *security.Chain, cp session.Checkpoint) error {
	if err := chain.CheckRestore(ctx, checkpointPaths(cp)); err != nil {
		return fmt.Errorf("undo blocked by security chain: %w", err)
	}
	return nil
}
