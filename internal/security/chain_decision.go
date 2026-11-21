package security

import (
	"context"
	"encoding/json"
	"strings"
)

// 本文件提供"决策表 → 链式守卫"的适配原语，把 decision_table.go 的 DecisionTable
// 包装成与 Chain.Check 同形的 Check(ctx, toolName, args) error。全部为纯函数：不修改
// 决策表、不修改 Chain、不产生副作用，且只依赖标准库。默认没有任何调用方。
//
// 关于类型名的取舍：任务语境中的 "Policy" 指 Check(ctx, toolName, args) error 这一
// 守卫契约，但本包已有的 Policy 是结构体，其 Check(name, args) 返回 Decision，签名并不
// 匹配；Chain 虽是同形契约的持有者，却是具体类型、不能作为接口参数，tools.Guard 又不能
// 被本包依赖（security 零依赖）。因此在包内定义最小接口 DecisionPolicy 作为适配目标，
// 任何同形守卫（包括 *Chain）都能作为 fallback 注入。

// DecisionPolicy 是链式守卫契约：与 Chain.Check、tools.Guard 同形的
// Check(ctx, toolName, args) error。拒绝以 ErrDenied 表达；nil 表示放行。
type DecisionPolicy interface {
	Check(ctx context.Context, toolName string, args json.RawMessage) error
}

// DecisionTableAsPolicy 把一张决策表包装成 DecisionPolicy。
//
// 返回的守卫在 Check 时把 (toolName, args) 投影成决策表可读的字符串上下文（见
// decisionContextKey），调用 table.Decide，并按结论映射：
//
//   - ActionAllow → nil（放行）
//   - ActionDeny  → ErrDenied（硬性拒绝，可用 errors.Is 匹配）
//   - ActionAsk   → 交给 fallback.Check
//
// table 为 nil 时完全退化为 fallback；fallback 为 nil 时任何需要它的路径都以
// ErrDenied 失败即拒绝，绝不把缺失的审批通道当成放行。未知 Action 同样失败即拒绝。
func DecisionTableAsPolicy(table *DecisionTable, fallback DecisionPolicy) DecisionPolicy {
	return &decisionTablePolicy{table: table, fallback: fallback}
}

// decisionTablePolicy 是 DecisionTableAsPolicy 返回的只读适配器。
type decisionTablePolicy struct {
	table    *DecisionTable
	fallback DecisionPolicy
}

// Check 实现 DecisionPolicy：先问决策表，再按映射决定是否委托给 fallback。
func (p *decisionTablePolicy) Check(ctx context.Context, toolName string, args json.RawMessage) error {
	if p.table == nil {
		return p.checkFallback(ctx, toolName, args)
	}
	action, _ := p.table.Decide(decisionContextKey(toolName, args))
	switch action {
	case ActionAllow:
		return nil
	case ActionAsk:
		return p.checkFallback(ctx, toolName, args)
	case ActionDeny:
		return ErrDenied
	default:
		// DecisionTable.Decide 只会返回三种已知 Action；仍以失败即拒绝兜底，
		// 避免未来扩展的取值被意外当成放行。
		return ErrDenied
	}
}

// checkFallback 把调用原样交给 fallback；缺失的 fallback 视为硬性拒绝。
func (p *decisionTablePolicy) checkFallback(ctx context.Context, toolName string, args json.RawMessage) error {
	if p.fallback == nil {
		return ErrDenied
	}
	return p.fallback.Check(ctx, toolName, args)
}

// decisionContextKey 把一次守卫调用投影成决策表的字符串上下文。
//
// 格式稳定为 "<工具名> <参数 JSON>"，参数为空（含空白与 "null"）时记作 "null"。
// 工具名两端的空白被去除，与 Policy.Evaluate 的 name 处理保持一致。
// context.Context 刻意不参与投影：决策只应依赖调用本身，不受取消、超时或上下文值
// 影响，从而保持可复现的纯函数语义；ctx 仍原样透传给 fallback。
func decisionContextKey(toolName string, args json.RawMessage) string {
	raw := strings.TrimSpace(string(args))
	if raw == "" || raw == "null" {
		raw = "null"
	}
	return strings.TrimSpace(toolName) + " " + raw
}
