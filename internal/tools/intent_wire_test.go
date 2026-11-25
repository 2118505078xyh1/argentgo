package tools

import (
	"encoding/json"
	"testing"

	"example.com/argentgo/internal/llm"
	"example.com/argentgo/internal/security"
)

// assertAdvertisesIntent 检查参数 schema 是否携带 `i` 意图
// 属性并将其列为必填 —— 这是 agent.ExtractIntent 在从传入
// 工具调用中剥离 `i` 时所依赖的契约。
func assertAdvertisesIntent(t *testing.T, raw json.RawMessage) {
	t.Helper()
	if !json.Valid(raw) {
		t.Fatalf("schema is not valid JSON: %s", raw)
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("unmarshal schema %s: %v", raw, err)
	}
	if _, ok := schema.Properties["i"]; !ok {
		t.Fatalf("schema does not advertise i: %s", raw)
	}
	for _, name := range schema.Required {
		if name == "i" {
			return
		}
	}
	t.Fatalf("schema does not require i: %s", raw)
}

func assertNoIntent(t *testing.T, raw json.RawMessage) {
	t.Helper()
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("unmarshal schema %s: %v", raw, err)
	}
	if _, ok := schema.Properties["i"]; ok {
		t.Fatalf("schema still advertises i: %s", raw)
	}
}

func TestIntentWireInjectAddsFieldToSampleSchema(t *testing.T) {
	in := json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)
	assertAdvertisesIntent(t, InjectIntentSchema(in))
}

func TestIntentWireAdvertiseIsIdempotent(t *testing.T) {
	in := json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)
	once := advertiseIntentSchema(in)
	twice := advertiseIntentSchema(once)
	if string(twice) != string(once) {
		t.Fatalf("second injection changed schema:\n got %s\nwant %s", twice, once)
	}
}

func TestIntentWireSpecsAndSpecsForAdvertiseIntent(t *testing.T) {
	registry := New(t.TempDir(), true)

	specs := registry.Specs()
	if len(specs) == 0 {
		t.Fatal("Specs() returned no tool specs")
	}
	for _, spec := range specs {
		assertAdvertisesIntent(t, spec.Function.Parameters)
	}

	for _, spec := range registry.SpecsFor(nil, security.SandboxWorkspaceWrite) {
		assertAdvertisesIntent(t, spec.Function.Parameters)
	}
}

func TestIntentWireSpecWithoutParamsIsLeftAlone(t *testing.T) {
	var spec llm.ToolSpec
	spec.Type = "function"
	spec.Function.Name = "no_params"
	if len(spec.Function.Parameters) != 0 {
		t.Fatalf("expected empty parameters, got %s", spec.Function.Parameters)
	}
	got := advertiseIntentSchema(spec.Function.Parameters)
	if len(got) != 0 {
		t.Fatalf("empty params should be left untouched, got %s", got)
	}
}

func TestIntentWireGateDisablesInjection(t *testing.T) {
	registry := New(t.TempDir(), true)

	// 默认（未设置）为注入开启。
	for _, spec := range registry.Specs() {
		assertAdvertisesIntent(t, spec.Function.Parameters)
	}

	// 真值的开关会在同一注册表上关闭注入（无缓存）。
	t.Setenv(intentSchemaEnv, "1")
	for _, spec := range registry.Specs() {
		assertNoIntent(t, spec.Function.Parameters)
	}
	for _, spec := range registry.SpecsFor(nil, security.SandboxWorkspaceWrite) {
		assertNoIntent(t, spec.Function.Parameters)
	}

	// 假值恢复注入。
	for _, value := range []string{"0", "false", "off", ""} {
		t.Setenv(intentSchemaEnv, value)
		for _, spec := range registry.Specs() {
			assertAdvertisesIntent(t, spec.Function.Parameters)
		}
	}
}
