package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ---- exit_plan ----

// exitPlanTool 实现模型可控的计划审批闭环。plan 只是给审批界面展示的文本，
// 本工具绝不执行它；真正的"解锁"动作由调用方通过 context 注入的回调完成。
// 这样 tools 包无需 import agent/app（依赖反转：回调在调用点注入）。
type exitPlanTool struct{}

func (exitPlanTool) Name() string { return "exit_plan" }

func (exitPlanTool) Description() string {
	return "Submit a plan for user approval; the plan text is display-only and is never executed."
}

// Mutating 必须为 false：这条工具不碰文件系统，否则会被只读沙箱与 --yolo 检查挡下。
func (exitPlanTool) Mutating() bool { return false }

func (exitPlanTool) Schema() string {
	return `{"type":"object","properties":{"plan":{"type":"string","description":"plan text shown for approval; display-only, never executed"}},"required":["plan"]}`
}

func (exitPlanTool) Run(ctx context.Context, root string, raw json.RawMessage) (string, error) {
	var args struct {
		Plan string `json:"plan"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("exit_plan: bad arguments: %w", err)
	}
	if strings.TrimSpace(args.Plan) == "" {
		return "", fmt.Errorf("exit_plan: missing required %q string", "plan")
	}
	// 回调缺失时报错而非静默成功，避免"模型以为已审批、实际什么都没发生"。
	unlock := PlanUnlockFrom(ctx)
	if unlock == nil {
		return "", fmt.Errorf("exit_plan: no plan unlock callback wired; approval cannot complete")
	}
	unlock()
	return "plan submitted for approval; awaiting user decision", nil
}

// RegisterExitPlan 把 exit_plan 显式追加到 r（opt-in）。它是幂等的：
// 重复调用或已存在同名工具时不再插入；已有工具顺序不变，新工具追加在末尾。
// 传入 nil 是空操作。
//
// 本函数绝不被 New/NewWithLimit 调用；只有需要计划审批闭环的调用方才会显式调用，
// 因此默认注册表的 Specs() 与 golden 快照完全一致。
func RegisterExitPlan(r *Registry) {
	if r == nil {
		return
	}
	const name = "exit_plan"
	if _, exists := r.tools[name]; exists {
		return
	}
	r.tools[name] = exitPlanTool{}
	r.order = append(r.order, name)
}
