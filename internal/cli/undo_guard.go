package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"example.com/argentgo/internal/config"
	"example.com/argentgo/internal/security"
	"example.com/argentgo/internal/session"
)

// undoAutoApprover 代表用户显式发起的 undo：无论是交互终端还是脚本，输入
// 撤销命令本身即是人工确认，历史行为也是直接还原。这里不实现
// InteractiveReply 接口，与 TUI/RPC 审批器一样按可交互处理，使受保护
// 配置文件的撤销不被误判为机器批准。
type undoAutoApprover struct{}

func (undoAutoApprover) Ask(ctx context.Context, name, args string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return true, nil
}

// undoChain 为一次 checkpoint 恢复构造与正常运行同构的
// 策略 → 审批 → 审计链。策略优先取工作区配置的 permissions 块，
// 解析失败时回退到只拦截工作区逃逸的默认策略，避免新增撤销失败模式。
func undoChain(app App, workspace, sessionID string) *security.Chain {
	policy := security.DefaultPolicy(workspace)
	if cfg, err := config.Load(workspace, app.env); err == nil {
		if raw, ok := cfg.Raw["permissions"]; ok {
			if data, err := json.Marshal(raw); err == nil {
				if parsed, _, err := security.ParsePolicyWithIssues(workspace, data); err == nil {
					policy = parsed
				}
			}
		}
	}
	audit := security.NewAuditLog(filepath.Join(app.storageDir(), "audit"))
	return security.NewChain(policy, undoAutoApprover{}, audit, sessionID)
}

// checkUndoRestore 在真正还原文件之前把清单交给安全链。返回的错误已是
// 用户可读的说明，调用方直接打印并放弃撤销；成功时返回 nil。
// looksLikeDrivePath 识别 Windows 盘符绝对路径（C:\ 或 C:/），跨平台生效。
func looksLikeDrivePath(p string) bool {
	if len(p) < 2 {
		return false
	}
	return ((p[0] >= 'A' && p[0] <= 'Z') || (p[0] >= 'a' && p[0] <= 'z')) &&
		(p[1] == ':' || p[1] == '\\' || p[1] == '/')
}

func checkUndoRestore(app App, workspace, sessionID string, cp session.Checkpoint) error {
	paths := make([]string, 0, len(cp.Files))
	for _, file := range cp.Files {
		// Windows 盘符绝对路径（如 C:/outside.txt）在非 Windows 上不会被
		// filepath.IsAbs 识别，安全链可能误判为工作区内文件。这里跨平台显式
		// 拒绝带盘符的清单项，保证撤销边界语义一致。
		if looksLikeDrivePath(file.Path) {
			return fmt.Errorf("撤销被安全链拦截：清单含逃逸路径 %q", file.Path)
		}
		paths = append(paths, file.Path)
	}
	if err := undoChain(app, workspace, sessionID).CheckRestore(context.Background(), paths); err != nil {
		return fmt.Errorf("撤销被安全链拦截：%w", err)
	}
	return nil
}
