package security

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// 编译期断言：本包既有 *Chain 的 Check 正是本适配器的目标契约；
// 自定义 recordingDecisionPolicy 同样满足，证明 fallback 可注入任意同形守卫。
var _ DecisionPolicy = (*Chain)(nil)
var _ DecisionPolicy = (*recordingDecisionPolicy)(nil)

// testCtxKey 是测试用的 context key 类型，避免 go vet 对基础类型键的告警。
type testCtxKey struct{}

// recordingDecisionPolicy 记录 fallback 的调用次数与入参，验证委托确实发生且原样透传。
type recordingDecisionPolicy struct {
	calls    int
	lastCtx  context.Context
	lastTool string
	lastArgs json.RawMessage
	err      error
}

func (r *recordingDecisionPolicy) Check(ctx context.Context, toolName string, args json.RawMessage) error {
	r.calls++
	r.lastCtx = ctx
	r.lastTool = toolName
	r.lastArgs = append(json.RawMessage(nil), args...)
	return r.err
}

func TestDecisionTableAsPolicy(t *testing.T) {
	allowTable := mustDecisionTable(t, []DecisionRule{AllowRule("allow-read", contains("read"), 1)})
	denyTable := mustDecisionTable(t, []DecisionRule{DenyRule("deny-secret", contains("secret"), 2)})
	askTable := mustDecisionTable(t, []DecisionRule{AskRule("ask-write", contains("write"), 3)})
	emptyTable := mustDecisionTable(t, nil)
	cjkAllow := mustDecisionTable(t, []DecisionRule{AllowRule("中文放行", contains("读取配置"), 1)})
	cjkDeny := mustDecisionTable(t, []DecisionRule{DenyRule("中文拒绝", contains("删除文件"), 1)})

	delegated := errors.New("fallback verdict")
	tests := []struct {
		name         string
		table        *DecisionTable
		tool         string
		args         string
		fallbackErr  error
		wantErr      error
		wantFallback int
	}{
		{"allow maps to nil", allowTable, "read", `{}`, nil, nil, 0},
		{"allow via args", allowTable, "ls", `{"note":"read config"}`, nil, nil, 0},
		{"deny maps to ErrDenied", denyTable, "read", `{"path":"top secret"}`, nil, ErrDenied, 0},
		{"ask delegates to fallback", askTable, "bash", `{"command":"write file"}`, delegated, delegated, 1},
		{"ask delegates nil verdict", askTable, "bash", `{"command":"write file"}`, nil, nil, 1},
		{"nil table delegates to fallback", nil, "ls", `{}`, delegated, delegated, 1},
		{"empty table fails closed", emptyTable, "read", `{}`, nil, ErrDenied, 0},
		{"no rule match fails closed", denyTable, "read", `{}`, nil, ErrDenied, 0},
		{"CJK tool allow", cjkAllow, "读取配置", `{}`, nil, nil, 0},
		{"CJK tool deny", cjkDeny, "删除文件", `{}`, nil, ErrDenied, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), testCtxKey{}, tc.name)
			fallback := &recordingDecisionPolicy{err: tc.fallbackErr}
			policy := DecisionTableAsPolicy(tc.table, fallback)

			err := policy.Check(ctx, tc.tool, json.RawMessage(tc.args))

			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("Check error = %v, want nil", err)
				}
			} else if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Check error = %v, want errors.Is(..., %v)", err, tc.wantErr)
			}
			if fallback.calls != tc.wantFallback {
				t.Fatalf("fallback calls = %d, want %d", fallback.calls, tc.wantFallback)
			}
			if tc.wantFallback > 0 {
				if fallback.lastCtx != ctx {
					t.Error("fallback received a different context")
				}
				if fallback.lastTool != tc.tool {
					t.Errorf("fallback tool = %q, want %q", fallback.lastTool, tc.tool)
				}
				if string(fallback.lastArgs) != tc.args {
					t.Errorf("fallback args = %s, want %s", fallback.lastArgs, tc.args)
				}
			}
		})
	}
}

func TestDecisionTableAsPolicyNilFallbackFailsClosed(t *testing.T) {
	askTable := mustDecisionTable(t, []DecisionRule{AskRule("ask", always(true), 1)})
	tests := []struct {
		name  string
		table *DecisionTable
		tool  string
	}{
		{"ask with nil fallback", askTable, "bash"},
		{"nil table with nil fallback", nil, "bash"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			policy := DecisionTableAsPolicy(tc.table, nil)
			if err := policy.Check(context.Background(), tc.tool, nil); !errors.Is(err, ErrDenied) {
				t.Fatalf("Check error = %v, want ErrDenied", err)
			}
		})
	}
}

func TestDecisionContextKey(t *testing.T) {
	tests := []struct {
		name string
		tool string
		args string
		want string
	}{
		{"object args", "read", `{"path":"a.txt"}`, `read {"path":"a.txt"}`},
		{"empty args", "ls", "", "ls null"},
		{"whitespace args", "ls", "   ", "ls null"},
		{"explicit null", "ls", "null", "ls null"},
		{"tool whitespace trimmed", "  read  ", `{}`, "read {}"},
		{"CJK tool and args", "读取配置", `{"路径":"x"}`, `读取配置 {"路径":"x"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := decisionContextKey(tc.tool, json.RawMessage(tc.args)); got != tc.want {
				t.Errorf("decisionContextKey(%q, %s) = %q, want %q", tc.tool, tc.args, got, tc.want)
			}
		})
	}
}

func TestDecisionTableAsPolicyDoesNotMutateTable(t *testing.T) {
	// 适配器只读：多次 Check 后，原决策表的判定保持不变。
	table := mustDecisionTable(t, []DecisionRule{DenyRule("d", contains("secret"), 1)})
	policy := DecisionTableAsPolicy(table, nil)
	for i := 0; i < 3; i++ {
		if err := policy.Check(context.Background(), "read", json.RawMessage(`{"path":"secret"}`)); !errors.Is(err, ErrDenied) {
			t.Fatalf("Check #%d error = %v, want ErrDenied", i, err)
		}
	}
	if action, name := table.Decide("read secret"); action != ActionDeny || name != "d" {
		t.Fatalf("table mutated: Decide = (%v, %q), want (%v, %q)", action, name, ActionDeny, "d")
	}
}
