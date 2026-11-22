package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// findCheck 按步骤名与类别取出唯一的检查结果。
func findCheck(t *testing.T, checks []ValidatorCheck, step, kind string) ValidatorCheck {
	t.Helper()
	var found []ValidatorCheck
	for _, c := range checks {
		if c.Step == step && c.Kind == kind {
			found = append(found, c)
		}
	}
	if len(found) != 1 {
		t.Fatalf("check(step=%q kind=%q) got %d results: %+v", step, kind, len(found), checks)
	}
	return found[0]
}

// TestCheckStepValidatorsTriState 覆盖 validator 通过/失败/缺失三态，
// 且同时覆盖 success_check 与 precondition 两类。
func TestCheckStepValidatorsTriState(t *testing.T) {
	def := mustDefinition(t, `{"steps":[
		{"name":"none","prompt":"a"},
		{"name":"good","prompt":"b","success_check":{"command":"exit 0"},"guard":{"preconditions":[{"key":"input","op":"contains","value":"x"}]}},
		{"name":"blank","prompt":"c","success_check":{"command":"   "}},
		{"name":"badguard","prompt":"d","guard":{"preconditions":[{"key":"","op":"equals","value":"v"}]}}
	]}`)

	checks := CheckStepValidators(def)
	// 每步固定两条：先 success_check，后 precondition；顺序与声明一致。
	wantOrder := []struct{ step, kind string }{
		{"none", ValidatorSuccessCheck}, {"none", ValidatorPrecondition},
		{"good", ValidatorSuccessCheck}, {"good", ValidatorPrecondition},
		{"blank", ValidatorSuccessCheck}, {"blank", ValidatorPrecondition},
		{"badguard", ValidatorSuccessCheck}, {"badguard", ValidatorPrecondition},
	}
	if len(checks) != len(wantOrder) {
		t.Fatalf("CheckStepValidators returned %d checks, want %d: %+v", len(checks), len(wantOrder), checks)
	}
	for i, want := range wantOrder {
		if checks[i].Step != want.step || checks[i].Kind != want.kind {
			t.Fatalf("checks[%d] = %+v, want step=%q kind=%q", i, checks[i], want.step, want.kind)
		}
	}

	cases := []struct {
		step, kind string
		wantState  string
		wantDetail bool
	}{
		{"none", ValidatorSuccessCheck, ValidatorMissing, false},
		{"none", ValidatorPrecondition, ValidatorMissing, false},
		{"good", ValidatorSuccessCheck, ValidatorPass, false},
		{"good", ValidatorPrecondition, ValidatorPass, false},
		{"blank", ValidatorSuccessCheck, ValidatorFail, true},
		{"blank", ValidatorPrecondition, ValidatorMissing, false},
		{"badguard", ValidatorSuccessCheck, ValidatorMissing, false},
		{"badguard", ValidatorPrecondition, ValidatorFail, true},
	}
	for _, tc := range cases {
		got := findCheck(t, checks, tc.step, tc.kind)
		if got.State != tc.wantState {
			t.Errorf("check(step=%q kind=%q).State = %q, want %q", tc.step, tc.kind, got.State, tc.wantState)
		}
		if (got.Detail != "") != tc.wantDetail {
			t.Errorf("check(step=%q kind=%q).Detail = %q, wantDetail=%v", tc.step, tc.kind, got.Detail, tc.wantDetail)
		}
		if tc.wantState == ValidatorFail && !strings.Contains(got.Detail, "步骤") {
			t.Errorf("failure detail should be a readable Chinese message, got %q", got.Detail)
		}
	}
}

// TestCheckStepValidatorsCJKDetail 验证含 CJK 步骤名与无效 op 的错误信息：
// CJK 字符原样保留、输出为合法 UTF-8、消息为中文。
func TestCheckStepValidatorsCJKDetail(t *testing.T) {
	def := mustDefinition(t, `{"steps":[
		{"name":"  校验中文步骤  ","prompt":"a","success_check":{"command":"  "}},
		{"name":"前置条件步骤","prompt":"b","guard":{"preconditions":[{"key":"input","op":"未知操作"}]}}
	]}`)

	checks := CheckStepValidators(def)

	blank := findCheck(t, checks, "校验中文步骤", ValidatorSuccessCheck)
	if blank.State != ValidatorFail {
		t.Fatalf("blank success_check state = %q, want fail", blank.State)
	}
	if !strings.Contains(blank.Detail, "校验中文步骤") {
		t.Errorf("CJK step name lost in detail: %q", blank.Detail)
	}
	if !utf8.ValidString(blank.Detail) {
		t.Errorf("detail is not valid UTF-8: %q", blank.Detail)
	}
	if !strings.Contains(blank.Detail, "不会校验") {
		t.Errorf("detail should explain the gap in Chinese: %q", blank.Detail)
	}

	badOp := findCheck(t, checks, "前置条件步骤", ValidatorPrecondition)
	if badOp.State != ValidatorFail {
		t.Fatalf("bad op precondition state = %q, want fail", badOp.State)
	}
	if !strings.Contains(badOp.Detail, "前置条件步骤") || !strings.Contains(badOp.Detail, "未知操作") {
		t.Errorf("CJK step name/op lost in detail: %q", badOp.Detail)
	}
	if !utf8.ValidString(badOp.Detail) {
		t.Errorf("detail is not valid UTF-8: %q", badOp.Detail)
	}
}

// TestCheckStepValidatorsUnknownOpAndNil 覆盖未知 op 与 nil 定义边界。
func TestCheckStepValidatorsUnknownOpAndNil(t *testing.T) {
	if got := CheckStepValidators(nil); got != nil {
		t.Fatalf("CheckStepValidators(nil) = %+v, want nil", got)
	}
	def := mustDefinition(t, `{"steps":[{"name":"a","prompt":"x","guard":{"preconditions":[{"key":"input","op":"not_an_op","value":"v"}]}}]}`)
	got := findCheck(t, CheckStepValidators(def), "a", ValidatorPrecondition)
	if got.State != ValidatorFail || !strings.Contains(got.Detail, "not_an_op") {
		t.Fatalf("unknown op check = %+v, want fail with op in detail", got)
	}
}

// TestCheckStepValidatorsDefaultOffByteForByteBaseline 守护"默认关"约束：
// 只读原语既不修改定义，也不影响默认执行路径的逐字节行为。
func TestCheckStepValidatorsDefaultOffByteForByteBaseline(t *testing.T) {
	doc := []byte(`{"name":"基线","steps":[{"name":"step","prompt":"do it","success_check":{"command":"   "}}]}`)
	def, err := ParseDefinition(doc)
	if err != nil {
		t.Fatalf("ParseDefinition: %v", err)
	}
	before, err := json.Marshal(def)
	if err != nil {
		t.Fatalf("marshal before: %v", err)
	}

	checks := CheckStepValidators(def)
	if got := findCheck(t, checks, "step", ValidatorSuccessCheck); got.State != ValidatorFail {
		t.Fatalf("gap not observed: %+v", got)
	}

	after, err := json.Marshal(def)
	if err != nil {
		t.Fatalf("marshal after: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("CheckStepValidators mutated the definition:\nbefore %s\nafter  %s", before, after)
	}

	// 默认执行路径不受影响：空白 success_check 仍被跳过，步骤成功、输出一致。
	// 这既是"默认关逐字节基线"的证据，也是被观测缺口的运行时表现。
	rec := &recorder{}
	exec := &Executor{Prompter: rec.prompter(func(string) (string, error) { return "unchanged", nil })}
	run, err := exec.Run(context.Background(), def, "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !run.Succeeded() || len(run.Steps) != 1 {
		t.Fatalf("run = %+v, want one successful step", run)
	}
	if got := run.Steps[0]; got.Output != "unchanged" || got.Error != "" || got.Attempts != 1 {
		t.Fatalf("default path changed: %+v", got)
	}
}
