package tools

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func exitPlanRunArgs(plan string) json.RawMessage {
	b, err := json.Marshal(map[string]string{"plan": plan})
	if err != nil {
		panic(err)
	}
	return b
}

func TestExitPlanToolContractAndSchema(t *testing.T) {
	tool := exitPlanTool{}
	if got := tool.Name(); got != "exit_plan" {
		t.Fatalf("Name() = %q, want exit_plan", got)
	}
	// 关键：非变更工具，不能被只读沙箱或 --yolo 检查挡下。
	if tool.Mutating() {
		t.Fatal("exit_plan must not be mutating")
	}
	raw := []byte(tool.Schema())
	if !json.Valid(raw) {
		t.Fatalf("Schema() is not valid JSON: %s", raw)
	}
	var schema struct {
		Type       string `json:"type"`
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	if schema.Type != "object" {
		t.Errorf("type = %q, want object", schema.Type)
	}
	if !reflect.DeepEqual(schema.Required, []string{"plan"}) {
		t.Errorf("required = %v, want [plan]", schema.Required)
	}
	prop, ok := schema.Properties["plan"]
	if !ok || prop.Type != "string" {
		t.Errorf("properties.plan = %+v, want type string", prop)
	}
}

func TestExitPlanUnlockCalled(t *testing.T) {
	r := New(t.TempDir(), false)
	called := 0
	r.SetPlanUnlock(func() { called++ })
	RegisterExitPlan(r)

	out, err := r.Run(context.Background(), "exit_plan", string(exitPlanRunArgs("先把改动拆成两步")))
	if err != nil {
		t.Fatalf("exit_plan run: %v", err)
	}
	if called != 1 {
		t.Fatalf("unlock called %d times, want exactly 1", called)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatal("empty output from exit_plan")
	}
	// 非 yolo 注册表也能成功，证明 Mutating()==false 绕过了 --yolo 闸门。
}

func TestExitPlanMissingCallbackFails(t *testing.T) {
	r := New(t.TempDir(), true)
	RegisterExitPlan(r)
	_, err := r.Run(context.Background(), "exit_plan", string(exitPlanRunArgs("第一步")))
	if err == nil || !strings.Contains(err.Error(), "no plan unlock callback") {
		t.Fatalf("err = %v, want missing-callback diagnostic", err)
	}
}

func TestExitPlanMissingPlanFails(t *testing.T) {
	r := New(t.TempDir(), true)
	r.SetPlanUnlock(func() {})
	RegisterExitPlan(r)

	for _, args := range []string{
		`{}`,
		`{"plan":"   "}`,
		`{"plan":123}`,
	} {
		called := false
		r.SetPlanUnlock(func() { called = true })
		_, err := r.Run(context.Background(), "exit_plan", args)
		if err == nil || !strings.Contains(err.Error(), "plan") {
			t.Errorf("args %s: err = %v, want plan diagnostic", args, err)
		}
		if called {
			t.Errorf("args %s: unlock must not fire for invalid plan", args)
		}
	}
}

func TestExitPlanCJKPlanText(t *testing.T) {
	r := New(t.TempDir(), false)
	called := false
	r.SetPlanUnlock(func() { called = true })
	RegisterExitPlan(r)

	plan := "计划：一、审阅现有实现；二、补测试；三、提交审查。"
	if _, err := r.Run(context.Background(), "exit_plan", string(exitPlanRunArgs(plan))); err != nil {
		t.Fatalf("CJK plan run: %v", err)
	}
	if !called {
		t.Fatal("unlock not called for CJK plan")
	}
}

func TestRegisterExitPlanIdempotentTailAndDefaultGolden(t *testing.T) {
	RegisterExitPlan(nil) // nil 空操作，不应 panic

	// 默认注册表绝不包含 exit_plan（不进 builtin_register.go）。
	baseline := New(t.TempDir(), true)
	beforeNames := baseline.Names()
	beforeSpecs := baseline.Specs()
	for _, name := range beforeNames {
		if name == "exit_plan" {
			t.Fatal("default New() must not contain exit_plan")
		}
	}
	for _, spec := range beforeSpecs {
		if spec.Function.Name == "exit_plan" {
			t.Fatal("default Specs() must not contain exit_plan")
		}
	}
	// 未 Register 的注册表不受另一份注册表注册行为的影响。
	if got := baseline.Names(); !reflect.DeepEqual(got, beforeNames) {
		t.Fatalf("baseline names drifted: %v", got)
	}

	r := New(t.TempDir(), true)
	before := r.Names()
	RegisterExitPlan(r)
	after := r.Names()
	if len(after) != len(before)+1 {
		t.Fatalf("after len = %d, want %d", len(after), len(before)+1)
	}
	if !reflect.DeepEqual(after[:len(before)], before) {
		t.Fatal("existing tool order changed after registration")
	}
	if got := after[len(after)-1]; got != "exit_plan" {
		t.Fatalf("last tool = %q, want exit_plan", got)
	}
	specs := r.Specs()
	if len(specs) != len(after) || specs[len(specs)-1].Function.Name != "exit_plan" {
		t.Fatalf("Specs() not consistent with Names() after registration")
	}
	RegisterExitPlan(r) // 幂等
	if got := r.Names(); !reflect.DeepEqual(got, after) {
		t.Fatalf("second registration changed names: %v", got)
	}
	if got := r.Specs(); !reflect.DeepEqual(got, specs) {
		t.Fatalf("second registration changed specs")
	}
}
