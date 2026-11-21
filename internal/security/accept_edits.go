package security

import (
	"context"
	"strings"
)

// ApprovalMode 表示审批模式。它只描述配置意图，不携带任何运行时状态。
type ApprovalMode string

// 支持的配置值。ModeAcceptEdits 必须显式选择才会启用，默认值仍是历史行为。
const (
	ModeAsk         ApprovalMode = "ask"
	ModeNever       ApprovalMode = "never"
	ModeAuto        ApprovalMode = "auto"
	ModeYolo        ApprovalMode = "yolo"
	ModeAcceptEdits ApprovalMode = "accept-edits"
)

// ParseApprovalMode 把配置字符串解析为 ApprovalMode。解析不区分大小写，
// 并忽略首尾空白；未知或空值回落到 ModeAsk，因此默认值保持历史行为。
// "accept_edits"、"accept edits" 等写法都归一化到 ModeAcceptEdits。
func ParseApprovalMode(s string) ApprovalMode {
	switch norm := strings.ToLower(strings.TrimSpace(s)); norm {
	case string(ModeAsk):
		return ModeAsk
	case string(ModeNever):
		return ModeNever
	case string(ModeAuto):
		return ModeAuto
	case string(ModeYolo):
		return ModeYolo
	case string(ModeAcceptEdits), "accept_edits", "accept edits", "acceptedits":
		return ModeAcceptEdits
	default:
		return ModeAsk
	}
}

// acceptEditsGrant 在 base 批准一次后，把工具名写入 scoped 的永久授予桶。
// 这样同一工具在包装器的生命周期内（即一个会话）不再询问；拒绝和错误都不缓存，
// 操作员仍可改变主意。作用域按工具名而非参数划分，因为 ScopedApprover 的
// GrantAlways 以名称为键。
type acceptEditsGrant struct {
	base   Approver
	scoped *ScopedApprover
}

func (g *acceptEditsGrant) Ask(ctx context.Context, name, args string) (bool, error) {
	ok, err := g.base.Ask(ctx, name, args)
	if err == nil && ok {
		g.scoped.GrantAlways(name)
	}
	return ok, err
}

// WrapForMode 按 mode 选择审批器。除 ModeAcceptEdits 外都原样返回 base，
// 逐字节保持既有行为；只有显式启用 accept-edits 时才叠加会话级作用域批准。
// 该函数是纯函数：不修改 base，也不持有调用方的可变状态；返回的包装器通过
// ScopedApprover 自身的互斥锁保证并发安全。
func WrapForMode(base Approver, mode ApprovalMode) Approver {
	if mode != ModeAcceptEdits {
		return base
	}
	if base == nil {
		base = NeverApprover{}
	}
	grant := &acceptEditsGrant{base: base}
	grant.scoped = NewScopedApprover(grant)
	return grant.scoped
}
