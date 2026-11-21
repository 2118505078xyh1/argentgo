package security

import "fmt"

// 本文件为决策表（见 decision_table.go）提供声明辅助原语：
//   - AllowRule / DenyRule / AskRule 用一条调用构造常见规则，减少调用方样板；
//   - ChainDecisions 把多张表按规则优先级合并成一张，支持"策略分层"式组合。
//
// 全部为纯函数：不产生副作用、不修改入参、不改变既有 DecisionTable 的语义，
// 且只依赖标准库。零值 / nil 行为与 decision_table.go 保持一致。

// AllowRule 构造一条"命中即放行"的规则。
//
// when 是由调用方注入的纯判定函数；传入 nil 时该规则被视为永不命中，最终由决策表
// 的失败即拒绝兜底。name 与 priority 原样填入，合法性由 NewDecisionTable 校验。
func AllowRule(name string, when func(string) bool, priority int) DecisionRule {
	return DecisionRule{Name: name, When: when, Action: ActionAllow, Priority: priority}
}

// DenyRule 构造一条"命中即一律拒绝"的规则。其余语义同 AllowRule。
func DenyRule(name string, when func(string) bool, priority int) DecisionRule {
	return DecisionRule{Name: name, When: when, Action: ActionDeny, Priority: priority}
}

// AskRule 构造一条"命中即交由审批"的规则。其余语义同 AllowRule。
func AskRule(name string, when func(string) bool, priority int) DecisionRule {
	return DecisionRule{Name: name, When: when, Action: ActionAsk, Priority: priority}
}

// TryChainDecisions 把多张决策表按规则优先级合并成一张新表，并返回可能的错误。
//
// 合并只读取各表的规则并构造新表，不会修改任何入参；结果沿用 NewDecisionTable 的
// 排序规则（优先级降序、同优先级名称升序），因此与传入表的先后顺序无关。nil 表与
// 空表被忽略。
//
// 单表内部已由 NewDecisionTable 保证名称唯一，所以重复只可能发生在表与表之间。
// 跨表重名时返回包裹 ErrDuplicateRuleName 哨兵的错误，调用方可用 errors.Is 判断。
func TryChainDecisions(tables ...*DecisionTable) (*DecisionTable, error) {
	merged := make([]DecisionRule, 0)
	for _, table := range tables {
		if table == nil {
			continue
		}
		merged = append(merged, table.rules...)
	}
	return NewDecisionTable(merged)
}

// ChainDecisions 是 TryChainDecisions 的便捷版本，签名只返回 *DecisionTable。
//
// 正常路径与空输入都与 TryChainDecisions 一致；由于无法在签名中返回 error，跨表
// 重名时以 panic 报告，panic 值可用 errors.Is 匹配 ErrDuplicateRuleName 哨兵。
// 需要以返回值处理冲突的调用方请改用 TryChainDecisions。
func ChainDecisions(tables ...*DecisionTable) *DecisionTable {
	chained, err := TryChainDecisions(tables...)
	if err != nil {
		// err 已包裹哨兵；再次包裹只补充上下文，errors.Is 仍可命中。
		panic(fmt.Errorf("security: chain decisions: %w", err))
	}
	return chained
}
