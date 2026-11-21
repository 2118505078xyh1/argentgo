package mcp

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// newDiagnosticSpec 构造一份 ToolSpec。Function 是匿名结构体，须逐字段赋值。
func newDiagnosticSpec(name, params string) llm.ToolSpec {
	var spec llm.ToolSpec
	spec.Type = "function"
	spec.Function.Name = name
	spec.Function.Parameters = json.RawMessage(params)
	return spec
}

// allowSet 返回一个只放行给定名字的 check，并记录每次被调用的名字。
func allowSet(names ...string) (func(string) bool, *[]string) {
	allowed := make(map[string]bool, len(names))
	for _, n := range names {
		allowed[n] = true
	}
	calls := make([]string, 0, len(names))
	return func(name string) bool {
		calls = append(calls, name)
		return allowed[name]
	}, &calls
}

// TestDiagnoseToolsHealthCombinations 表驱动覆盖可用性与 schema 合法性的四种组合，
// 断言 Degraded 只在“可用但 schema 非法”时为真，并校验中英双语 Reason。
func TestDiagnoseToolsHealthCombinations(t *testing.T) {
	validSchema := `{"type":"object","properties":{}}`
	invalidSchema := `{"type":`

	specs := []llm.ToolSpec{
		newDiagnosticSpec("ok", validSchema),
		newDiagnosticSpec("degraded", invalidSchema),
		newDiagnosticSpec("unavailable", validSchema),
		newDiagnosticSpec("down", invalidSchema),
	}
	check, _ := allowSet("ok", "degraded")

	got := DiagnoseTools(specs, check)
	want := []ToolHealth{
		{Name: "ok", Available: true, SchemaValid: true, Degraded: false, Reason: "正常/ok"},
		{Name: "degraded", Available: true, SchemaValid: false, Degraded: true, Reason: "schema 非法/invalid schema"},
		{Name: "unavailable", Available: false, SchemaValid: true, Degraded: false, Reason: "不可用/unavailable"},
		{Name: "down", Available: false, SchemaValid: false, Degraded: false, Reason: "不可用/unavailable; schema 非法/invalid schema"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DiagnoseTools() = %+v, want %+v", got, want)
	}
}

// TestDiagnoseToolsInvalidSchema 覆盖多种非法/边界 schema：截断 JSON、空字节、
// 裸文本为非法；合法 JSON（含 CJK 内容）为合法。
func TestDiagnoseToolsInvalidSchema(t *testing.T) {
	specs := []llm.ToolSpec{
		newDiagnosticSpec("truncated", `{"a":`),
		newDiagnosticSpec("empty", ""),
		newDiagnosticSpec("nil-schema", ""), // RawMessage(nil) 等价空切片
		newDiagnosticSpec("plain-text", `not json`),
		newDiagnosticSpec("valid-cjk", `{"描述":"中文","n":1}`),
		newDiagnosticSpec("valid-null", `null`),
	}
	specs[2].Function.Parameters = nil

	got := DiagnoseTools(specs, func(string) bool { return true })
	wantValid := []bool{false, false, false, false, true, true}
	for i, h := range got {
		if h.SchemaValid != wantValid[i] {
			t.Errorf("spec %q SchemaValid = %v, want %v", specs[i].Function.Name, h.SchemaValid, wantValid[i])
		}
		if h.Available != true {
			t.Errorf("spec %q Available = false, want true", specs[i].Function.Name)
		}
	}
	if got[4].Degraded || got[5].Degraded {
		t.Errorf("合法 schema 且可用时不应降级: got[4]=%+v got[5]=%+v", got[4], got[5])
	}
	if got[0].Degraded != true || got[4].Degraded != false {
		t.Errorf("降级判定错误: got[0].Degraded=%v want true; got[4].Degraded=%v want false",
			got[0].Degraded, got[4].Degraded)
	}
}

// TestDiagnoseToolsInjectedCheck 验证 check 被每个工具调用一次，且收到完全一致的名字。
func TestDiagnoseToolsInjectedCheck(t *testing.T) {
	specs := []llm.ToolSpec{
		newDiagnosticSpec("alpha", `{}`),
		newDiagnosticSpec("beta", `{}`),
		newDiagnosticSpec("gamma", `{}`),
	}
	check, calls := allowSet("alpha", "gamma")
	got := DiagnoseTools(specs, check)

	wantCalls := []string{"alpha", "beta", "gamma"}
	if !reflect.DeepEqual(*calls, wantCalls) {
		t.Errorf("check 调用序列 = %v, want %v", *calls, wantCalls)
	}
	if !got[0].Available || got[1].Available || !got[2].Available {
		t.Errorf("注入 check 的可用性判定错误: %+v", got)
	}
}

// TestDiagnoseToolsNilCheck 验证 check 为 nil 时不 panic，且所有工具视为不可用。
func TestDiagnoseToolsNilCheck(t *testing.T) {
	specs := []llm.ToolSpec{newDiagnosticSpec("a", `{}`), newDiagnosticSpec("b", `{}`)}
	got := DiagnoseTools(specs, nil)
	for _, h := range got {
		if h.Available || h.Degraded {
			t.Errorf("nil check 应判为不可用且非降级: %+v", h)
		}
		if h.Reason == "正常/ok" {
			t.Errorf("nil check 的 Reason 不应为正常: %+v", h)
		}
	}
}

// TestDiagnoseToolsCJKNames 验证 CJK 工具名被 check 原样接收，且诊断结果逐字节保留名称。
func TestDiagnoseToolsCJKNames(t *testing.T) {
	specs := []llm.ToolSpec{
		newDiagnosticSpec("读取文件", `{"描述":"中文参数"}`),
		newDiagnosticSpec("英文-tool", `{}`),
		newDiagnosticSpec("🌏混合", `{"k":"值"}`),
	}
	check, calls := allowSet("读取文件", "🌏混合")
	got := DiagnoseTools(specs, check)

	wantCalls := []string{"读取文件", "英文-tool", "🌏混合"}
	if !reflect.DeepEqual(*calls, wantCalls) {
		t.Errorf("check 收到的名字 = %v, want %v", *calls, wantCalls)
	}
	for i := range specs {
		if got[i].Name != specs[i].Function.Name {
			t.Errorf("名称被破坏: got %q, want %q", got[i].Name, specs[i].Function.Name)
		}
	}
	if !got[0].Available || got[1].Available || !got[2].Available {
		t.Errorf("CJK 可用性判定错误: %+v", got)
	}
	if !got[0].SchemaValid || !got[2].SchemaValid {
		t.Errorf("CJK schema 合法性判定错误: %+v", got)
	}
}

// TestDiagnoseToolsStableOrder 验证输出顺序与输入一致，且重复调用结果逐字节相同。
func TestDiagnoseToolsStableOrder(t *testing.T) {
	specs := []llm.ToolSpec{
		newDiagnosticSpec("zeta", `{}`),
		newDiagnosticSpec("服务甲", `{}`),
		newDiagnosticSpec("alpha", `{}`),
	}
	check, _ := allowSet("服务甲", "alpha")
	want := DiagnoseTools(specs, check)
	for i := 0; i < 50; i++ {
		got := DiagnoseTools(specs, check)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("第 %d 次诊断结果不稳定: got %+v, want %+v", i, got, want)
		}
	}
	for i := range specs {
		if want[i].Name != specs[i].Function.Name {
			t.Fatalf("输出顺序漂移: 位置 %d 应为 %q, 实为 %q", i, specs[i].Function.Name, want[i].Name)
		}
	}
}

// TestDiagnoseToolsDoesNotMutateInputs 验证不修改入参，且输出与入参无别名。
func TestDiagnoseToolsDoesNotMutateInputs(t *testing.T) {
	specs := []llm.ToolSpec{
		newDiagnosticSpec("a", `{"x":1}`),
		newDiagnosticSpec("b", `{"y":"中文"}`),
	}
	// 深拷贝入参（含 RawMessage 字节）作为对照。
	before := make([]llm.ToolSpec, len(specs))
	for i := range specs {
		before[i] = specs[i]
		before[i].Function.Parameters = append(json.RawMessage(nil), specs[i].Function.Parameters...)
	}

	got := DiagnoseTools(specs, func(string) bool { return true })
	if !reflect.DeepEqual(specs, before) {
		t.Errorf("DiagnoseTools 修改了入参: got %+v, want %+v", specs, before)
	}

	// 修改输出不应回写入参。
	got[0].Name = "mutated"
	got[0].Reason = "mutated"
	got[0].Available = false
	if !reflect.DeepEqual(specs, before) {
		t.Errorf("修改输出污染了入参: got %+v, want %+v", specs, before)
	}
}

// TestDiagnoseToolsEmpty 验证 nil 与空切片输入得到空结果。
func TestDiagnoseToolsEmpty(t *testing.T) {
	for _, specs := range [][]llm.ToolSpec{nil, {}} {
		got := DiagnoseTools(specs, func(string) bool { return true })
		if got == nil {
			t.Error("DiagnoseTools 空输入应返回非 nil 空切片")
		}
		if len(got) != 0 {
			t.Errorf("DiagnoseTools 空输入长度 = %d, want 0", len(got))
		}
	}
}

// TestToolHealthSummary 表驱动覆盖摘要的计数与稳定中文格式。
func TestToolHealthSummary(t *testing.T) {
	tests := []struct {
		name string
		hs   []ToolHealth
		want string
	}{
		{name: "空输入", hs: nil, want: "工具诊断：共 0 个，可用 0，不可用 0，schema 合法 0，schema 非法 0，降级 0。"},
		{
			name: "混合",
			hs: []ToolHealth{
				{Name: "ok", Available: true, SchemaValid: true},
				{Name: "degraded", Available: true, SchemaValid: false, Degraded: true},
				{Name: "unavailable", Available: false, SchemaValid: true},
				{Name: "down", Available: false, SchemaValid: false},
			},
			want: "工具诊断：共 4 个，可用 2，不可用 2，schema 合法 2，schema 非法 2，降级 1。",
		},
		{
			name: "CJK 名不影响计数",
			hs: []ToolHealth{
				{Name: "服务甲", Available: true, SchemaValid: true},
				{Name: "服务乙", Available: false, SchemaValid: false},
			},
			want: "工具诊断：共 2 个，可用 1，不可用 1，schema 合法 1，schema 非法 1，降级 0。",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ToolHealthSummary(tt.hs); got != tt.want {
				t.Errorf("ToolHealthSummary() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestToolHealthSummaryStable 验证摘要与顺序无关、重复调用逐字节一致。
func TestToolHealthSummaryStable(t *testing.T) {
	hs := []ToolHealth{
		{Name: "a", Available: true, SchemaValid: true},
		{Name: "b", Available: false, SchemaValid: false},
		{Name: "c", Available: true, SchemaValid: false, Degraded: true},
	}
	want := ToolHealthSummary(hs)
	for i := 0; i < 50; i++ {
		if got := ToolHealthSummary(hs); got != want {
			t.Fatalf("第 %d 次摘要不稳定: %q, want %q", i, got, want)
		}
	}
	reversed := []ToolHealth{hs[2], hs[1], hs[0]}
	if got := ToolHealthSummary(reversed); got != want {
		t.Errorf("摘要受顺序影响: %q, want %q", got, want)
	}
}
