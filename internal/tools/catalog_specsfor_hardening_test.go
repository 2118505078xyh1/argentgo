package tools

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"example.com/argentgo/internal/security"
)

// TestSpecsForClampsLongDescription 检查经策略过滤后的目录
// 是否应用与 Specs 相同的描述长度上限。过长的 MCP/工具描述是
// 提示注入的载体，因此每一条跨越传输层的目录路径都必须
// 截断它 —— 而不只是 Specs。
func TestSpecsForClampsLongDescription(t *testing.T) {
	r := New(t.TempDir(), true)
	const name = "specsfor_desc_cap"
	long := strings.Repeat("中😀", maxToolDescLen)
	// 在 wrapSchemas 之后注册（如同 app 的 Set* 助手那样），使工具
	// 不被包装，从而 SpecsFor 无法依赖装饰器来截断其描述。
	r.tools[name] = descCapFakeTool{name: name, desc: long}
	r.order = append(r.order, name)

	found := false
	for _, spec := range r.SpecsFor(nil, security.SandboxWorkspaceWrite) {
		if spec.Function.Name != name {
			continue
		}
		found = true
		if got := utf8.RuneCountInString(spec.Function.Description); got != maxToolDescLen {
			t.Fatalf("SpecsFor description has %d runes, want %d", got, maxToolDescLen)
		}
		if want := clipDescription(long); spec.Function.Description != want {
			t.Fatal("SpecsFor did not clamp the long description")
		}
	}
	if !found {
		t.Fatal("fake tool missing from SpecsFor")
	}
}

// TestSpecsForAdvertisesIntentForLateWiredTools 检查在 New 之后被替换的工具
// （通过 Set* 助手替换的 usage/history/checkpoint_diff）仍在
// 经策略过滤的目录中公布 `i` 意图字段。Specs() 会显式注入它；
// SpecsFor 过去仅依赖构造时安装的包装器，因此
// 后接入的工具会悄悄丢失它。
func TestSpecsForAdvertisesIntentForLateWiredTools(t *testing.T) {
	r := New(t.TempDir(), true)
	r.SetUsageSource(&fakeUsageSource{json: `{"tokens":0}`})
	r.SetHistorySource(&fakeHistorySource{result: ""})
	r.SetCheckpointSource(&fakeCheckpointSource{})

	for _, spec := range r.SpecsFor(nil, security.SandboxWorkspaceWrite) {
		switch spec.Function.Name {
		case "usage", "history", "checkpoint_diff":
			var schema struct {
				Properties map[string]json.RawMessage `json:"properties"`
				Required   []string                   `json:"required"`
			}
			if err := json.Unmarshal(spec.Function.Parameters, &schema); err != nil {
				t.Fatalf("%s schema is invalid: %v", spec.Function.Name, err)
			}
			if _, ok := schema.Properties["i"]; !ok {
				t.Fatalf("%s schema does not advertise i: %s", spec.Function.Name, spec.Function.Parameters)
			}
			required := false
			for _, n := range schema.Required {
				if n == "i" {
					required = true
				}
			}
			if !required {
				t.Fatalf("%s schema does not require i: %s", spec.Function.Name, spec.Function.Parameters)
			}
		}
	}

	// 该开关仍通过 SpecsFor 遵守操作员的 opt-out。
	t.Setenv(intentSchemaEnv, "1")
	for _, spec := range r.SpecsFor(nil, security.SandboxWorkspaceWrite) {
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(spec.Function.Parameters, &schema); err != nil {
			t.Fatalf("%s schema is invalid: %v", spec.Function.Name, err)
		}
		if _, ok := schema.Properties["i"]; ok {
			t.Fatalf("%s still advertises i with the gate disabled", spec.Function.Name)
		}
	}
}

// TestSpecsForAndSpecsAgreeOnLateWiredTool 断言这样一个契约：在相同注册表状态下，
// 两个目录构建器产出完全一致的 spec。
func TestSpecsForAndSpecsAgreeOnLateWiredTool(t *testing.T) {
	r := New(t.TempDir(), true)
	r.SetUsageSource(&fakeUsageSource{json: `{"tokens":0}`})
	want := r.Specs()
	got := r.SpecsFor(nil, security.SandboxWorkspaceWrite)
	for i := range want {
		if want[i].Function.Name != got[i].Function.Name {
			continue
		}
		if want[i].Function.Description != got[i].Function.Description ||
			string(want[i].Function.Parameters) != string(got[i].Function.Parameters) {
			t.Fatalf("specs disagree for %s:\n Specs:    %+v\n SpecsFor: %+v",
				want[i].Function.Name, want[i].Function, got[i].Function)
		}
	}
}
