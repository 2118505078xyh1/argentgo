package tools

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// TestSpecsForClampsLongDescription checks that the policy-filtered catalog
// applies the same description cap as Specs. A long MCP/tool description is a
// prompt-injection vector, so every catalog path that crosses the wire must
// clamp it — not just Specs.
func TestSpecsForClampsLongDescription(t *testing.T) {
	r := New(t.TempDir(), true)
	const name = "specsfor_desc_cap"
	long := strings.Repeat("中😀", maxToolDescLen)
	// Register after wrapSchemas (as app Set* helpers do) so the tool is not
	// wrapped and SpecsFor cannot rely on a decorator to clamp its description.
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

// TestSpecsForAdvertisesIntentForLateWiredTools checks that tools replaced after
// New (usage/history/checkpoint_diff via Set* helpers) still advertise the `i`
// intent field in the policy-filtered catalog. Specs() injects it explicitly;
// SpecsFor used to depend solely on the wrapper installed at construction, so
// late-wired tools silently lost it.
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

	// The gate still honours the operator opt-out through SpecsFor as well.
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

// TestSpecsForAndSpecsAgreeOnLateWiredTool asserts the contract that the two
// catalog builders produce identical specs for the same registry state.
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
