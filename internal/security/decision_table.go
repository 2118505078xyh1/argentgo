package security

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// 本文件提供声明式"策略决策表"原语：把 allow/deny/ask 三种互斥结论建模为一张
// 按优先级排序的规则表。每条规则携带一个由调用方注入的纯判定函数 When；决策时
// 按优先级从高到低评估，首个 When 命中的规则给出结论，无一命中则失败即拒绝
// （deny）。
//
// 这是全新且自包含的原语：只依赖标准库，零调用方，不接入 Chain.Check，也不改变
// 既有"策略 → 审批 → 审计"安全链的任何行为。零值 DecisionTable 表示"没有规则"，
// Decide 会失败即拒绝。
//
// 注意命名：本包已有 policy.Rule（工具/路径/命令的声明式策略行），且它已被
// internal/app、internal/tools 等外部包引用，不能重命名。因此这里用 DecisionRule
// 作为新原语的行类型，保持完全 additive，避免破坏既有 API。

var (
	// ErrEmptyRuleName 表示规则名为空（含仅空白）。
	ErrEmptyRuleName = errors.New("security: decision rule name is empty")
	// ErrDuplicateRuleName 表示同一张表内出现重名规则。
	ErrDuplicateRuleName = errors.New("security: decision rule name is duplicated")
)

// DefaultRuleName 是无任何规则命中时失败即拒绝所返回的兜底规则名。
const DefaultRuleName = "default-deny"

// Action 是决策表的一条结论：allow 放行、ask 交由审批器、deny 一律拒绝。
// 它与既有的 Decision 枚举相互独立，避免把协议细节带进本原语。
type Action int

const (
	// ActionAllow 命中该规则时放行。
	ActionAllow Action = iota
	// ActionDeny 命中该规则时一律拒绝。
	ActionDeny
	// ActionAsk 命中该规则时交由审批。
	ActionAsk
)

// String 返回稳定的小写名称，未知取值按失败即拒绝返回 "deny"。
func (a Action) String() string {
	switch a {
	case ActionAllow:
		return "allow"
	case ActionAsk:
		return "ask"
	default:
		return "deny"
	}
}

// DecisionRule 是决策表中的一行。
//
// Name 必须非空且在表内唯一：既用于构造时报错定位，也作为 Decide 返回的命中
// 规则名。When 是由调用方注入的纯函数，只读取 ctx、不产生副作用；nil 被视为
// 永不命中。Action 是命中时给出的结论，Priority 越大越优先。
type DecisionRule struct {
	Name     string
	When     func(ctx string) bool
	Action   Action
	Priority int
}

// DecisionTable 是一张不可变的规则表。构造时即完成校验与排序，之后只读。
// 零值可用：没有规则时 Decide 返回失败即拒绝。
type DecisionTable struct {
	// rules 已按 Priority 降序、同优先级按 Name 升序预排序。
	rules []DecisionRule
}

// NewDecisionTable 构造一张决策表。
//
// 它会复制传入的切片（调用方后续修改切片不会影响本表），校验规则名非空且互不
// 重复，并预排序为"优先级降序，同优先级名称升序"。重名或空名时返回可用
// errors.Is 匹配的哨兵错误。
func NewDecisionTable(rules []DecisionRule) (*DecisionTable, error) {
	seen := make(map[string]struct{}, len(rules))
	copied := make([]DecisionRule, len(rules))
	for i, rule := range rules {
		if strings.TrimSpace(rule.Name) == "" {
			return nil, fmt.Errorf("%w at index %d", ErrEmptyRuleName, i)
		}
		if _, exists := seen[rule.Name]; exists {
			return nil, fmt.Errorf("%w: %q", ErrDuplicateRuleName, rule.Name)
		}
		seen[rule.Name] = struct{}{}
		copied[i] = rule
	}
	sort.Slice(copied, func(i, j int) bool {
		if copied[i].Priority != copied[j].Priority {
			return copied[i].Priority > copied[j].Priority
		}
		return copied[i].Name < copied[j].Name
	})
	return &DecisionTable{rules: copied}, nil
}

// Decide 返回首个命中规则的 Action 与规则名。评估顺序由 NewDecisionTable 预置：
// 优先级降序，同优先级名称升序。When 为 nil 的规则被跳过。没有任何规则命中（或
// 调用方传入 nil 表）时返回 ActionDeny 与 DefaultRuleName，保持失败即拒绝。
func (d *DecisionTable) Decide(ctx string) (Action, string) {
	if d == nil {
		return ActionDeny, DefaultRuleName
	}
	for _, rule := range d.rules {
		if rule.When == nil {
			continue
		}
		if rule.When(ctx) {
			return rule.Action, rule.Name
		}
	}
	return ActionDeny, DefaultRuleName
}
