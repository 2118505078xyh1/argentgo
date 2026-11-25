package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"example.com/argentgo/internal/security"
)

// 编译期锁定：守卫检查器必须能直接放进 security.InspectionManager。
var _ security.Inspector = (*guardianInspector)(nil)

// guardianEnv 构造一个只回答守卫键的环境读取器，避免测试读取真实环境变量。
func guardianEnv(value string) func(string) string {
	return func(key string) string {
		if key == guardianEnvKey {
			return value
		}
		return ""
	}
}

// TestGuardianInspectorVerdictMapping 覆盖审阅函数返回的每种动作到检查判定的映射。
func TestGuardianInspectorVerdictMapping(t *testing.T) {
	tests := []struct {
		name       string
		review     security.InspectionAction
		wantAction security.InspectionAction
	}{
		{name: "allow", review: security.InspectAllow, wantAction: security.InspectAllow},
		{name: "require approval", review: security.InspectRequireApproval, wantAction: security.InspectRequireApproval},
		{name: "deny", review: security.InspectDeny, wantAction: security.InspectDeny},
		// 未知动作必须失败即拒绝，不能被当作 Allow。
		{name: "unknown action", review: security.InspectionAction(99), wantAction: security.InspectRequireApproval},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			review := func(ctx context.Context, tool, args string) (security.Verdict, error) {
				calls++
				if tool != "write" {
					t.Errorf("tool = %q, want write", tool)
				}
				if args != `{"path":"x"}` {
					t.Errorf("args = %q", args)
				}
				return security.Verdict{Action: tt.review, Reason: "模型输出"}, nil
			}
			g := newGuardianInspector(review, guardianEnv("1"))

			got, err := g.Inspect(context.Background(), "write", json.RawMessage(`{"path":"x"}`))
			if err != nil {
				t.Fatalf("Inspect() error = %v", err)
			}
			if got.Action != tt.wantAction {
				t.Errorf("Action = %v, want %v", got.Action, tt.wantAction)
			}
			if got.Inspector != guardianInspectorName {
				t.Errorf("Inspector = %q, want %q", got.Inspector, guardianInspectorName)
			}
			if got.Confidence != 1 {
				t.Errorf("Confidence = %v, want 1", got.Confidence)
			}
			if calls != 1 {
				t.Errorf("review calls = %d, want 1", calls)
			}
		})
	}
}

// TestGuardianInspectorFailClosed 覆盖审阅器缺失、报错、context 取消/超时等异常：
// 一律 RequireApproval，绝不 Allow。
func TestGuardianInspectorFailClosed(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	defer cancelExpired()

	allow := func(context.Context, string, string) (security.Verdict, error) {
		return security.Verdict{Action: security.InspectAllow}, nil
	}
	fail := func(context.Context, string, string) (security.Verdict, error) {
		return security.Verdict{}, errors.New("reviewer blew up")
	}

	tests := []struct {
		name       string
		ctx        context.Context
		review     guardianReviewFunc
		wantCalls  int
		wantSubstr string
	}{
		{name: "nil reviewer", ctx: context.Background(), review: nil, wantCalls: 0, wantSubstr: "unavailable"},
		{name: "review error", ctx: context.Background(), review: fail, wantCalls: 1, wantSubstr: "failed"},
		{name: "canceled context", ctx: canceled, review: allow, wantCalls: 0, wantSubstr: "interrupted"},
		{name: "expired deadline", ctx: expired, review: allow, wantCalls: 0, wantSubstr: "interrupted"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			var review guardianReviewFunc
			if tt.review != nil {
				review = func(ctx context.Context, tool, args string) (security.Verdict, error) {
					calls++
					return tt.review(ctx, tool, args)
				}
			}
			g := newGuardianInspector(review, guardianEnv("1"))

			got, err := g.Inspect(tt.ctx, "bash", json.RawMessage(`{"command":"rm -rf /"}`))
			if err != nil {
				t.Fatalf("Inspect() error = %v, want nil (fail-closed via verdict)", err)
			}
			if got.Action != security.InspectRequireApproval {
				t.Errorf("Action = %v, want RequireApproval", got.Action)
			}
			if calls != tt.wantCalls {
				t.Errorf("review calls = %d, want %d", calls, tt.wantCalls)
			}
			if !strings.Contains(got.Reason, tt.wantSubstr) {
				t.Errorf("Reason = %q, want to contain %q", got.Reason, tt.wantSubstr)
			}
		})
	}
}

// TestGuardianInspectorPostReviewCancellation 保证审阅返回后 context 若已过期，
// 迟到的 Allow 判定也不会被采信。
func TestGuardianInspectorPostReviewCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	review := func(context.Context, string, string) (security.Verdict, error) {
		calls++
		cancel() // 审阅过程中 context 被取消。
		return security.Verdict{Action: security.InspectAllow}, nil
	}
	g := newGuardianInspector(review, guardianEnv("1"))

	got, err := g.Inspect(ctx, "write", nil)
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if got.Action != security.InspectRequireApproval {
		t.Errorf("Action = %v, want RequireApproval", got.Action)
	}
	if calls != 1 {
		t.Errorf("review calls = %d, want 1", calls)
	}
}

// TestGuardianInspectorReadOnlySkipsReview 覆盖只读工具：直接 Allow，不调用审阅函数，
// 即使审阅函数会返回 Deny 也不影响结果。
func TestGuardianInspectorReadOnlySkipsReview(t *testing.T) {
	for _, tool := range []string{
		"read", "ls", "grep", "find", "glob", "todo", "web_fetch", "git_status",
		"  READ  ", // 大小写与空白归一化后仍是只读工具。
	} {
		t.Run(tool, func(t *testing.T) {
			calls := 0
			review := func(context.Context, string, string) (security.Verdict, error) {
				calls++
				return security.Verdict{Action: security.InspectDeny}, nil
			}
			g := newGuardianInspector(review, guardianEnv("1"))

			got, err := g.Inspect(context.Background(), tool, nil)
			if err != nil {
				t.Fatalf("Inspect() error = %v", err)
			}
			if got.Action != security.InspectAllow {
				t.Errorf("Action = %v, want Allow", got.Action)
			}
			if calls != 0 {
				t.Errorf("review calls = %d, want 0 for read-only tool", calls)
			}
		})
	}
}

// TestGuardianInspectorEnvGate 覆盖 ARGENT_GUARDIAN 环境门：只有值恰为 "1"
// （允许首尾空白）才启用审阅，其余值都退化为不增加关卡的 Allow。
func TestGuardianInspectorEnvGate(t *testing.T) {
	tests := []struct {
		name        string
		env         func(string) string
		wantEnabled bool
	}{
		{name: "1 enables", env: guardianEnv("1"), wantEnabled: true},
		{name: "padded 1 enables", env: guardianEnv(" \t1 "), wantEnabled: true},
		{name: "0 disables", env: guardianEnv("0"), wantEnabled: false},
		{name: "true disables", env: guardianEnv("true"), wantEnabled: false},
		{name: "empty disables", env: guardianEnv(""), wantEnabled: false},
		{name: "nil accessor disables", env: nil, wantEnabled: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			review := func(context.Context, string, string) (security.Verdict, error) {
				calls++
				return security.Verdict{Action: security.InspectDeny}, nil
			}
			g := newGuardianInspector(review, tt.env)
			got, err := g.Inspect(context.Background(), "write", nil)
			if err != nil {
				t.Fatalf("Inspect() error = %v", err)
			}
			if tt.wantEnabled {
				if got.Action != security.InspectDeny || calls != 1 {
					t.Errorf("enabled: Action = %v, calls = %d; want Deny/1", got.Action, calls)
				}
				return
			}
			if got.Action != security.InspectAllow || calls != 0 {
				t.Errorf("disabled: Action = %v, calls = %d; want Allow/0", got.Action, calls)
			}
		})
	}
}
