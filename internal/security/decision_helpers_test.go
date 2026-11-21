package security

import (
	"errors"
	"testing"
)

// mustDecisionTable 在测试中构造决策表；构造失败直接终止当前测试。
func mustDecisionTable(t *testing.T, rules []DecisionRule) *DecisionTable {
	t.Helper()
	table, err := NewDecisionTable(rules)
	if err != nil {
		t.Fatalf("NewDecisionTable: %v", err)
	}
	return table
}

func TestRuleConstructors(t *testing.T) {
	tests := []struct {
		name       string
		build      func() DecisionRule
		wantAction Action
	}{
		{"allow", func() DecisionRule { return AllowRule("r", contains("x"), 3) }, ActionAllow},
		{"deny", func() DecisionRule { return DenyRule("r", contains("x"), 3) }, ActionDeny},
		{"ask", func() DecisionRule { return AskRule("r", contains("x"), 3) }, ActionAsk},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rule := tc.build()
			if rule.Name != "r" || rule.Priority != 3 || rule.Action != tc.wantAction {
				t.Fatalf("rule = %+v, want name=r priority=3 action=%v", rule, tc.wantAction)
			}
			// when 应被原样透传，nil 以外的判定函数保持调用方语义。
			if rule.When == nil || !rule.When("axb") || rule.When("zzz") {
				t.Fatalf("When not passed through as contains(%q)", "x")
			}
		})
	}
}

func TestRuleConstructorsNilWhen(t *testing.T) {
	// nil when 应被原样保留；由决策表按既有语义视为永不命中。
	rules := []DecisionRule{
		AllowRule("nil-allow", nil, 99),
		DenyRule("nil-deny", nil, 98),
		AskRule("nil-ask", nil, 97),
	}
	for _, rule := range rules {
		if rule.When != nil {
			t.Errorf("%s.When = non-nil, want nil", rule.Name)
		}
	}
	table := mustDecisionTable(t, rules)
	action, name := table.Decide("anything")
	if action != ActionDeny || name != DefaultRuleName {
		t.Fatalf("Decide = (%v, %q), want (%v, %q)", action, name, ActionDeny, DefaultRuleName)
	}
}

func TestConstructedRulesDecide(t *testing.T) {
	// 三个构造器产出的规则放进同一张表，验证 Action 在真实决策中生效。
	table := mustDecisionTable(t, []DecisionRule{
		AllowRule("allow-read", contains("read"), 1),
		AskRule("ask-write", contains("write"), 2),
		DenyRule("deny-secret", contains("secret"), 3),
	})
	tests := []struct {
		ctx      string
		want     Action
		wantName string
	}{
		{"read config", ActionAllow, "allow-read"},
		{"write config", ActionAsk, "ask-write"},
		{"top secret", ActionDeny, "deny-secret"},
		{"nothing", ActionDeny, DefaultRuleName},
	}
	for _, tc := range tests {
		gotAction, gotName := table.Decide(tc.ctx)
		if gotAction != tc.want || gotName != tc.wantName {
			t.Errorf("Decide(%q) = (%v, %q), want (%v, %q)", tc.ctx, gotAction, gotName, tc.want, tc.wantName)
		}
	}
}

func TestChainDecisionsMergesByPriority(t *testing.T) {
	base := mustDecisionTable(t, []DecisionRule{
		AllowRule("low", always(true), 1),
		DenyRule("high", always(true), 10),
	})
	extra := mustDecisionTable(t, []DecisionRule{
		AskRule("mid", always(true), 5),
	})
	tests := []struct {
		name   string
		tables []*DecisionTable
	}{
		{"base then extra", []*DecisionTable{base, extra}},
		{"extra then base", []*DecisionTable{extra, base}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			chained := ChainDecisions(tc.tables...)
			// 合并顺序由规则优先级决定，与表的传入顺序无关：high 恒胜出。
			action, name := chained.Decide("anything")
			if action != ActionDeny || name != "high" {
				t.Fatalf("Decide = (%v, %q), want (%v, %q)", action, name, ActionDeny, "high")
			}
		})
	}
}

func TestChainDecisionsSamePriorityNameAscending(t *testing.T) {
	// 跨表同优先级时按名称升序决胜，同样与表的传入顺序无关。
	a := mustDecisionTable(t, []DecisionRule{AskRule("zeta", always(true), 5)})
	b := mustDecisionTable(t, []DecisionRule{DenyRule("alpha", always(true), 5)})
	tests := []struct {
		name   string
		tables []*DecisionTable
	}{
		{"a then b", []*DecisionTable{a, b}},
		{"b then a", []*DecisionTable{b, a}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			action, name := ChainDecisions(tc.tables...).Decide("x")
			if action != ActionDeny || name != "alpha" {
				t.Fatalf("Decide = (%v, %q), want (%v, %q)", action, name, ActionDeny, "alpha")
			}
		})
	}
}

func TestChainDecisionsRejectsDuplicateNames(t *testing.T) {
	a := mustDecisionTable(t, []DecisionRule{AllowRule("dup", always(true), 1)})
	b := mustDecisionTable(t, []DecisionRule{DenyRule("dup", always(false), 2)})
	tests := []struct {
		name   string
		tables []*DecisionTable
	}{
		{"chain panic", []*DecisionTable{a, b}},
		{"try returns error", []*DecisionTable{a, b}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "chain panic" {
				defer func() {
					r := recover()
					if r == nil {
						t.Fatal("ChainDecisions did not panic on duplicate names")
					}
					err, ok := r.(error)
					if !ok {
						t.Fatalf("panic value = %v (%T), want error", r, r)
					}
					if !errors.Is(err, ErrDuplicateRuleName) {
						t.Fatalf("errors.Is(err, ErrDuplicateRuleName) = false for %v", err)
					}
				}()
				ChainDecisions(tc.tables...)
				return
			}
			_, err := TryChainDecisions(tc.tables...)
			if !errors.Is(err, ErrDuplicateRuleName) {
				t.Fatalf("err = %v, want errors.Is(err, ErrDuplicateRuleName)", err)
			}
		})
	}
}

func TestChainDecisionsEmptyAndNil(t *testing.T) {
	tests := []struct {
		name   string
		tables []*DecisionTable
	}{
		{"no args", nil},
		{"single nil", []*DecisionTable{nil}},
		{"multiple nil", []*DecisionTable{nil, nil}},
		{"empty table", []*DecisionTable{mustDecisionTable(t, nil)}},
		{"nil and empty", []*DecisionTable{nil, mustDecisionTable(t, []DecisionRule{})}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			chained := ChainDecisions(tc.tables...)
			if chained == nil {
				t.Fatal("ChainDecisions returned nil")
			}
			action, name := chained.Decide("x")
			if action != ActionDeny || name != DefaultRuleName {
				t.Fatalf("Decide = (%v, %q), want (%v, %q)", action, name, ActionDeny, DefaultRuleName)
			}
			// TryChainDecisions 对同样的输入也不应报错。
			if _, err := TryChainDecisions(tc.tables...); err != nil {
				t.Fatalf("TryChainDecisions: %v", err)
			}
		})
	}
}

func TestChainDecisionsCJKNames(t *testing.T) {
	allow := mustDecisionTable(t, []DecisionRule{AllowRule("中文放行", contains("写入配置"), 1)})
	ask := mustDecisionTable(t, []DecisionRule{AskRule("中文询问", contains("删除文件"), 5)})
	chained := ChainDecisions(allow, ask)
	tests := []struct {
		ctx      string
		want     Action
		wantName string
	}{
		{"允许写入配置文件", ActionAllow, "中文放行"},
		{"正在删除文件", ActionAsk, "中文询问"},
		{"无关上下文", ActionDeny, DefaultRuleName},
	}
	for _, tc := range tests {
		gotAction, gotName := chained.Decide(tc.ctx)
		if gotAction != tc.want || gotName != tc.wantName {
			t.Errorf("Decide(%q) = (%v, %q), want (%v, %q)", tc.ctx, gotAction, gotName, tc.want, tc.wantName)
		}
	}
}

func TestChainDecisionsDoesNotMutateInputs(t *testing.T) {
	// 两张表各有一条在 "shared" 上下文下命中的规则，优先级不同。合并后高优先级胜出，
	// 但两张入参表自身的判定结果必须保持不变。
	a := mustDecisionTable(t, []DecisionRule{AllowRule("a-rule", contains("shared"), 1)})
	b := mustDecisionTable(t, []DecisionRule{DenyRule("b-rule", contains("shared"), 10)})
	chained := ChainDecisions(a, b)

	if action, name := a.Decide("shared"); action != ActionAllow || name != "a-rule" {
		t.Fatalf("input a mutated: Decide = (%v, %q)", action, name)
	}
	if action, name := b.Decide("shared"); action != ActionDeny || name != "b-rule" {
		t.Fatalf("input b mutated: Decide = (%v, %q)", action, name)
	}
	if action, name := chained.Decide("shared"); action != ActionDeny || name != "b-rule" {
		t.Fatalf("chained Decide = (%v, %q), want (%v, %q)", action, name, ActionDeny, "b-rule")
	}
}
