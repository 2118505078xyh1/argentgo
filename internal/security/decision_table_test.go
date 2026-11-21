package security

import (
	"errors"
	"strings"
	"testing"
)

// always 返回一个恒为给定的 When，便于在表里直接声明规则的命中情况。
func always(want bool) func(string) bool {
	return func(string) bool { return want }
}

// contains 返回一个检查 ctx 是否包含子串的 When。
func contains(substr string) func(string) bool {
	return func(ctx string) bool { return len(substr) > 0 && strings.Contains(ctx, substr) }
}

func TestDecisionTablePriorityDescending(t *testing.T) {
	// 输入顺序刻意打乱：高优先级规则即使排在最后也应先被评估并胜出。
	rules := []DecisionRule{
		{Name: "low", When: always(true), Action: ActionAllow, Priority: 1},
		{Name: "high", When: always(true), Action: ActionDeny, Priority: 10},
		{Name: "mid", When: always(true), Action: ActionAsk, Priority: 5},
	}
	table, err := NewDecisionTable(rules)
	if err != nil {
		t.Fatalf("NewDecisionTable: %v", err)
	}
	gotAction, gotName := table.Decide("anything")
	if gotAction != ActionDeny || gotName != "high" {
		t.Fatalf("Decide = (%v, %q), want (%v, %q)", gotAction, gotName, ActionDeny, "high")
	}
}

func TestDecisionTableSamePriorityNameAscending(t *testing.T) {
	// 同优先级时按名称升序："alpha" 应先命中，即使它在输入里排在后面。
	rules := []DecisionRule{
		{Name: "zeta", When: always(true), Action: ActionAllow, Priority: 7},
		{Name: "alpha", When: always(true), Action: ActionDeny, Priority: 7},
		{Name: "beta", When: always(true), Action: ActionAsk, Priority: 7},
	}
	table, err := NewDecisionTable(rules)
	if err != nil {
		t.Fatalf("NewDecisionTable: %v", err)
	}
	gotAction, gotName := table.Decide("anything")
	if gotAction != ActionDeny || gotName != "alpha" {
		t.Fatalf("Decide = (%v, %q), want (%v, %q)", gotAction, gotName, ActionDeny, "alpha")
	}
}

func TestDecisionTableFailClosedWhenNoMatch(t *testing.T) {
	tests := []struct {
		name  string
		rules []DecisionRule
	}{
		{"empty table", nil},
		{"no rule matches", []DecisionRule{
			{Name: "allow-read", When: always(false), Action: ActionAllow, Priority: 1},
			{Name: "ask-write", When: always(false), Action: ActionAsk, Priority: 2},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			table, err := NewDecisionTable(tc.rules)
			if err != nil {
				t.Fatalf("NewDecisionTable: %v", err)
			}
			action, name := table.Decide("ctx")
			if action != ActionDeny || name != DefaultRuleName {
				t.Fatalf("Decide = (%v, %q), want (%v, %q)", action, name, ActionDeny, DefaultRuleName)
			}
		})
	}
}

func TestDecisionTableRejectsDuplicateNames(t *testing.T) {
	_, err := NewDecisionTable([]DecisionRule{
		{Name: "dup", When: always(true), Action: ActionAllow, Priority: 2},
		{Name: "dup", When: always(false), Action: ActionDeny, Priority: 1},
	})
	if !errors.Is(err, ErrDuplicateRuleName) {
		t.Fatalf("err = %v, want errors.Is(err, ErrDuplicateRuleName)", err)
	}
}

func TestDecisionTableRejectsEmptyNames(t *testing.T) {
	tests := []struct {
		name  string
		rules []DecisionRule
	}{
		{"empty string", []DecisionRule{{Name: "", When: always(true), Action: ActionAllow}}},
		{"whitespace only", []DecisionRule{{Name: " \t ", When: always(true), Action: ActionAllow}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewDecisionTable(tc.rules)
			if !errors.Is(err, ErrEmptyRuleName) {
				t.Fatalf("err = %v, want errors.Is(err, ErrEmptyRuleName)", err)
			}
		})
	}
}

func TestDecisionTableInjectedWhen(t *testing.T) {
	// When 由调用方注入，决策完全取决于注入的纯函数对 ctx 的判断。
	table, err := NewDecisionTable([]DecisionRule{
		{Name: "prefix-gold", When: contains("gold"), Action: ActionAllow, Priority: 1},
		{Name: "prefix-iron", When: contains("iron"), Action: ActionAsk, Priority: 1},
	})
	if err != nil {
		t.Fatalf("NewDecisionTable: %v", err)
	}
	tests := []struct {
		ctx      string
		want     Action
		wantName string
	}{
		{"golden path", ActionAllow, "prefix-gold"},
		{"iron path", ActionAsk, "prefix-iron"},
		{"none", ActionDeny, DefaultRuleName},
	}
	for _, tc := range tests {
		gotAction, gotName := table.Decide(tc.ctx)
		if gotAction != tc.want || gotName != tc.wantName {
			t.Errorf("Decide(%q) = (%v, %q), want (%v, %q)", tc.ctx, gotAction, gotName, tc.want, tc.wantName)
		}
	}
}

func TestDecisionTableCJKContext(t *testing.T) {
	// 中文 ctx 原样传给注入的 When，不做任何编码或裁剪。
	table, err := NewDecisionTable([]DecisionRule{
		{Name: "中文放行", When: contains("写入配置"), Action: ActionAllow, Priority: 1},
		{Name: "中文询问", When: contains("删除文件"), Action: ActionAsk, Priority: 1},
	})
	if err != nil {
		t.Fatalf("NewDecisionTable: %v", err)
	}
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
		gotAction, gotName := table.Decide(tc.ctx)
		if gotAction != tc.want || gotName != tc.wantName {
			t.Errorf("Decide(%q) = (%v, %q), want (%v, %q)", tc.ctx, gotAction, gotName, tc.want, tc.wantName)
		}
	}
}

func TestDecisionTableSkipsNilWhen(t *testing.T) {
	// nil When 视为永不命中；最终由失败即拒绝兜底。
	table, err := NewDecisionTable([]DecisionRule{
		{Name: "nil-when", When: nil, Action: ActionAllow, Priority: 99},
	})
	if err != nil {
		t.Fatalf("NewDecisionTable: %v", err)
	}
	action, name := table.Decide("anything")
	if action != ActionDeny || name != DefaultRuleName {
		t.Fatalf("Decide = (%v, %q), want (%v, %q)", action, name, ActionDeny, DefaultRuleName)
	}
}

func TestDecisionTableCopiesInput(t *testing.T) {
	// 构造后修改调用方切片不应改变表的判定结果。
	rules := []DecisionRule{{Name: "orig", When: always(true), Action: ActionAllow, Priority: 1}}
	table, err := NewDecisionTable(rules)
	if err != nil {
		t.Fatalf("NewDecisionTable: %v", err)
	}
	rules[0] = DecisionRule{Name: "changed", When: always(true), Action: ActionDeny, Priority: 100}
	action, name := table.Decide("anything")
	if action != ActionAllow || name != "orig" {
		t.Fatalf("Decide = (%v, %q), want (%v, %q)", action, name, ActionAllow, "orig")
	}
}

func TestDecisionTableNilReceiverFailsClosed(t *testing.T) {
	var table *DecisionTable
	action, name := table.Decide("anything")
	if action != ActionDeny || name != DefaultRuleName {
		t.Fatalf("Decide = (%v, %q), want (%v, %q)", action, name, ActionDeny, DefaultRuleName)
	}
}

func TestActionString(t *testing.T) {
	tests := []struct {
		action Action
		want   string
	}{
		{ActionAllow, "allow"},
		{ActionDeny, "deny"},
		{ActionAsk, "ask"},
		{Action(99), "deny"}, // 未知取值按失败即拒绝
	}
	for _, tc := range tests {
		if got := tc.action.String(); got != tc.want {
			t.Errorf("Action(%d).String() = %q, want %q", tc.action, got, tc.want)
		}
	}
}
