package tools

import (
	"encoding/json"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/security"
)

// assertAdvertisesIntent checks that a parameter schema carries the `i` intent
// property and lists it as required — the contract agent.ExtractIntent relies
// on when it strips `i` from incoming tool calls.
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

	// Default (unset) is injection ON.
	for _, spec := range registry.Specs() {
		assertAdvertisesIntent(t, spec.Function.Parameters)
	}

	// A truthy gate disables injection on the very same registry (no caching).
	t.Setenv(intentSchemaEnv, "1")
	for _, spec := range registry.Specs() {
		assertNoIntent(t, spec.Function.Parameters)
	}
	for _, spec := range registry.SpecsFor(nil, security.SandboxWorkspaceWrite) {
		assertNoIntent(t, spec.Function.Parameters)
	}

	// Falsy values restore injection.
	for _, value := range []string{"0", "false", "off", ""} {
		t.Setenv(intentSchemaEnv, value)
		for _, spec := range registry.Specs() {
			assertAdvertisesIntent(t, spec.Function.Parameters)
		}
	}
}
