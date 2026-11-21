package app

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/security"
)

// countingReview 是一个可注入的假审阅函数，记录调用次数并返回预设判定。
type countingReview struct {
	calls   int
	verdict security.Verdict
	err     error
}

func (r *countingReview) fn(_ context.Context, _, _ string) (security.Verdict, error) {
	r.calls++
	return r.verdict, r.err
}

func guardianTestIO(values map[string]string) cli.IO {
	return cli.IO{Env: envFrom(values)}
}

// TestGuardianEnabledEnvValues 验证 env 门：只有精确值 "1"（允许首尾空白）才开启。
func TestGuardianEnabledEnvValues(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{name: "nil env", env: nil, want: false},
		{name: "unset", env: map[string]string{}, want: false},
		{name: "zero", env: map[string]string{"ARGENT_GUARDIAN": "0"}, want: false},
		{name: "empty", env: map[string]string{"ARGENT_GUARDIAN": ""}, want: false},
		{name: "true text", env: map[string]string{"ARGENT_GUARDIAN": "true"}, want: false},
		{name: "double one", env: map[string]string{"ARGENT_GUARDIAN": "01"}, want: false},
		{name: "one", env: map[string]string{"ARGENT_GUARDIAN": "1"}, want: true},
		{name: "padded one", env: map[string]string{"ARGENT_GUARDIAN": " 1 "}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var env func(string) string
			if tc.env != nil {
				env = envFrom(tc.env)
			}
			if got := guardianEnabled(env); got != tc.want {
				t.Fatalf("guardianEnabled = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestGuardianInspectorForConditions 验证注册是三条件"与"：环境门、次要模型、审阅函数
// 缺一不可。返回非 nil 才代表守卫会被挂到 InspectionManager 上。
func TestGuardianInspectorForConditions(t *testing.T) {
	review := func(context.Context, string, string) (security.Verdict, error) {
		return security.Verdict{Action: security.InspectAllow}, nil
	}
	on := envFrom(map[string]string{"ARGENT_GUARDIAN": "1"})
	off := envFrom(map[string]string{"ARGENT_GUARDIAN": "0"})

	cases := []struct {
		name           string
		env            func(string) string
		model          string
		review         guardianReviewFunc
		wantRegistered bool
	}{
		{name: "all set", env: on, model: "weak-model", review: review, wantRegistered: true},
		{name: "guardian off", env: off, model: "weak-model", review: review},
		{name: "nil env", env: nil, model: "weak-model", review: review},
		{name: "empty model", env: on, model: "", review: review},
		{name: "blank model", env: on, model: "   ", review: review},
		{name: "nil review", env: on, model: "weak-model", review: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := guardianInspectorFor(tc.env, tc.model, tc.review)
			if tc.wantRegistered && got == nil {
				t.Fatal("guardian inspector should be registered")
			}
			if !tc.wantRegistered && got != nil {
				t.Fatalf("guardian inspector should not be registered, got %v", got.Name())
			}
		})
	}
}

// TestInspectionGuardForTwoSwitches 验证两开关的"与"关系：ARGENT_GUARDIAN=1 但
// ARGENT_INSPECTION≠1 时 guard 仍是裸链，Guardian 不生效。
func TestInspectionGuardForTwoSwitches(t *testing.T) {
	approver := &countingApprover{grant: true}
	chain := askChain(t.TempDir(), approver)
	fake := &countingReview{verdict: security.Verdict{Action: security.InspectAllow}}

	// 总开关关闭：即便 Guardian 的门与模型都打开，也必须返回裸链。
	bare := inspectionGuardFor(chain, approver,
		guardianTestIO(map[string]string{"ARGENT_GUARDIAN": "1"}), "weak-model", fake.fn)
	if bare != chain {
		t.Fatalf("inspection off: guard = %T, want the bare chain", bare)
	}
	if _, ok := bare.(*inspectionGuard); ok {
		t.Fatal("inspection off must not wrap the chain")
	}

	// 总开关开启：返回检查门。Guardian 是否真正注册由 Guardian 自身条件决定。
	wrapped := inspectionGuardFor(chain, approver,
		guardianTestIO(map[string]string{"ARGENT_INSPECTION": "1", "ARGENT_GUARDIAN": "1"}),
		"weak-model", fake.fn)
	g, ok := wrapped.(*inspectionGuard)
	if !ok {
		t.Fatalf("inspection on: guard = %T, want *inspectionGuard", wrapped)
	}
	if g.chain != chain || g.manager == nil {
		t.Fatal("inspection guard must retain the original chain and a manager")
	}
}

// TestGuardianWiringDefaultOffByteIdentical 验证默认（两开关都关）行为与基线一致：
// guard 就是链本身，注入的审阅函数完全不会被调用。
func TestGuardianWiringDefaultOffByteIdentical(t *testing.T) {
	approver := &countingApprover{grant: true}
	chain := askChain(t.TempDir(), approver)
	fake := &countingReview{verdict: security.Verdict{Action: security.InspectAllow}}

	guard := inspectionGuardFor(chain, approver, guardianTestIO(nil), "weak-model", fake.fn)
	if guard != chain {
		t.Fatal("default wiring must return the bare chain")
	}
	args := json.RawMessage(`{"command":"echo hi"}`)
	ctx := context.Background()
	wantErr := chain.Check(ctx, "bash", args)
	gotErr := guard.Check(ctx, "bash", args)
	if (gotErr == nil) != (wantErr == nil) {
		t.Fatalf("guard.Check = %v, chain.Check = %v; want identical behavior", gotErr, wantErr)
	}
	if fake.calls != 0 {
		t.Fatalf("reviewer called %d times with guardian off, want 0", fake.calls)
	}
}

// TestGuardianWiringNoSecondaryModel 验证守卫开启但没有次要模型时不注册：
// 审阅函数一次都不会被调用。
func TestGuardianWiringNoSecondaryModel(t *testing.T) {
	approver := &countingApprover{grant: true}
	chain := askChain(t.TempDir(), approver)
	fake := &countingReview{verdict: security.Verdict{Action: security.InspectAllow}}

	guard := newInspectionGuard(chain, approver,
		guardianTestIO(map[string]string{"ARGENT_GUARDIAN": "1"}), "", fake.fn)
	guard.manager.Resolve(context.Background(), "bash", json.RawMessage(`{"command":"echo hi"}`))
	if fake.calls != 0 {
		t.Fatalf("reviewer called %d times without a secondary model, want 0", fake.calls)
	}
}

// TestGuardianWiringReviewCalledOnce 验证两开关都开且有次要模型时守卫被注册：
// 一次检查恰好调用审阅函数一次。
func TestGuardianWiringReviewCalledOnce(t *testing.T) {
	approver := &countingApprover{grant: true}
	chain := askChain(t.TempDir(), approver)
	fake := &countingReview{verdict: security.Verdict{Action: security.InspectAllow}}

	guard := newInspectionGuard(chain, approver,
		guardianTestIO(map[string]string{"ARGENT_GUARDIAN": "1", "ARGENT_INSPECTION": "1"}),
		"weak-model", fake.fn)
	guard.manager.Resolve(context.Background(), "bash", json.RawMessage(`{"command":"echo hi"}`))
	if fake.calls != 1 {
		t.Fatalf("reviewer called %d times, want exactly 1", fake.calls)
	}
}

// TestDefaultGuardianReviewFailClosed 验证默认审阅器永不返回 Allow。
func TestDefaultGuardianReviewFailClosed(t *testing.T) {
	got, err := defaultGuardianReview(context.Background(), "bash", `{"command":"rm -rf /"}`)
	if err != nil {
		t.Fatalf("default review returned an error: %v", err)
	}
	if got.Action != security.InspectRequireApproval {
		t.Fatalf("default review action = %v, want InspectRequireApproval", got.Action)
	}
}
