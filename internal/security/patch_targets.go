package security

import (
	"encoding/json"
	"fmt"
	"strings"
)

// CheckPatchTargets 把从补丁正文中解析出的真实目标路径逐条交给既有策略评估。
// 它只做策略判定与审计，不触发审批：审批仍由整次工具调用的 Chain.Check 完成。
// 任一目标被拒绝即返回 StopError，调用方应在工具执行之前停止，从而做到零写入。
//
// 这是 apply_patch 路径策略绕过缺口的支撑：补丁把目标藏在 input 字符串里，
// Policy.Evaluate 默认的路径参数扫描看不到其中的文件，导致用户配置的
// denyPaths/allowPaths 以及任何带 Path 的规则静默失效。app 守卫用
// tools.ParseApplyPatch 解析出目标列表后调用本方法；工具自身的 resolve 仍负责
// 工作区边界，本方法补上的是「按路径的策略规则」这一环。
func (c *Chain) CheckPatchTargets(tool string, paths []string, auditArgs json.RawMessage) error {
	if c == nil || len(paths) == 0 {
		return nil
	}
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		raw, err := json.Marshal(map[string]any{"path": path})
		if err != nil {
			return fmt.Errorf("security: encoding patch target %q: %w", path, err)
		}
		result := c.policy().Evaluate(tool, raw)
		if result.Decision == Deny {
			c.record(tool, "deny", result.Reason, auditArgs)
			return &StopError{Tool: tool, Reason: result.Reason}
		}
	}
	return nil
}
