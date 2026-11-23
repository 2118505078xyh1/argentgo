package security

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// callProfileKey 是携带单次调用权限 profile 的 context key。
type callProfileKey struct{}

// WithCallProfile 把 profile 放进 ctx，使后续 Chain.Check 用它收紧判定。
func WithCallProfile(ctx context.Context, p CallProfile) context.Context {
	return context.WithValue(ctx, callProfileKey{}, p)
}

// CallProfileFrom 从 ctx 取出 profile；未注入时返回零值（无额外约束）。
func CallProfileFrom(ctx context.Context) CallProfile {
	if ctx == nil {
		return CallProfile{}
	}
	if p, ok := ctx.Value(callProfileKey{}).(CallProfile); ok {
		return p
	}
	return CallProfile{}
}

// CallProfile 描述单次工具调用的额外权限约束。它叠加在全局策略之上，
// 只收紧不放宽：deny 优先于全局 write，ReadOnly 拒绝所有修改类工具。
// 零值表示无额外约束，CheckWith 退化为普通 Check。
type CallProfile struct {
	// DenyPaths 是额外的路径 deny 模式（glob）；命中即 Deny。
	DenyPaths []string
	// DenyCommands 是额外的命令子串模式；命中即 Deny。
	DenyCommands []string
	// ReadOnly 为 true 时拒绝所有修改类工具（如 danger-full-access 之外的只读调用）。
	ReadOnly bool
	// Network 为 false 时拒绝已知的网络访问工具。
	NoNetwork bool
}

var networkTools = map[string]bool{
	"web_fetch": true, "web_search": true, "http": true, "curl": true,
}

// IsZero 报告 profile 是否无额外约束（与未注入等价）。
func (p CallProfile) IsZero() bool {
	return !p.ReadOnly && !p.NoNetwork && len(p.DenyPaths) == 0 && len(p.DenyCommands) == 0
}

// CheckWith 按 profile 评估一次调用：先应用每次调用的额外约束（只收紧），
// 再走既有 Evaluate。它不修改 Policy 或入参，默认空 profile 与 Check 等价。
func (p *Policy) CheckWith(name string, args json.RawMessage, profile CallProfile) Decision {
	if r := p.evaluateWithProfile(name, args, profile); r.Decision != Allow {
		return r.Decision
	}
	return p.Evaluate(name, args).Decision
}

// checkWithResult 是 CheckWith 的带原因版本，便于诊断。
func (p *Policy) checkWithResult(name string, args json.RawMessage, profile CallProfile) Result {
	r := p.evaluateWithProfile(name, args, profile)
	if r.Decision != Allow {
		return r
	}
	return p.Evaluate(name, args)
}

func (p *Policy) evaluateWithProfile(name string, args json.RawMessage, profile CallProfile) Result {
	clean := strings.TrimSpace(name)
	if profile.ReadOnly && MutatingTool(clean) {
		return Result{Deny, fmt.Sprintf("the tool %q is read-only for this call", name)}
	}
	if profile.NoNetwork && networkTools[clean] {
		return Result{Deny, fmt.Sprintf("the tool %q is network-blocked for this call", name)}
	}
	fields, err := parseArgs(args)
	if err != nil {
		return Result{Allow, ""} // 参数畸形交给 Evaluate 处理
	}
	for _, path := range pathValues(fields) {
		for _, pattern := range profile.DenyPaths {
			if GlobMatch(pattern, path) {
				return Result{Deny, fmt.Sprintf("the path %q is denied by the per-call profile (rule %q)", path, pattern)}
			}
		}
	}
	if command, ok := fields["command"].(string); ok {
		for _, pattern := range profile.DenyCommands {
			if strings.Contains(command, pattern) {
				return Result{Deny, fmt.Sprintf("the command is denied by the per-call profile (rule %q)", pattern)}
			}
		}
	}
	return Result{Allow, ""}
}
