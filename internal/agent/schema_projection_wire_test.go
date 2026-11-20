package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

func specWithParams(name, params string) llm.ToolSpec {
	var spec llm.ToolSpec
	spec.Type = "function"
	spec.Function.Name = name
	spec.Function.Parameters = json.RawMessage(params)
	return spec
}

// TestAdvertisedSpecsProjectsSchemaForModel checks that advertisedSpecs applies
// the right projection based on the session model name, that unrelated models
// keep their schemas byte-for-byte, and that the toolset's catalog is untouched.
func TestAdvertisedSpecsProjectsSchemaForModel(t *testing.T) {
	tests := []struct {
		name      string
		model     string
		params    string
		wantIn    []string
		wantExact string // when set, Parameters must equal this exactly
	}{
		{
			name:   "moonshot types an untyped enum",
			model:  "moonshot-v1-8k",
			params: `{"enum":["a","b"]}`,
			wantIn: []string{`"type":"string"`},
		},
		{
			name:   "kimi types an untyped enum",
			model:  "kimi-k2-0905",
			params: `{"enum":["a","b"]}`,
			wantIn: []string{`"type":"string"`},
		},
		{
			name:      "gemini gains an object root",
			model:     "gemini-2.0-flash",
			params:    `{}`,
			wantExact: `{"type":"object"}`,
		},
		{
			name:      "unknown model keeps bytes",
			model:     "gpt-4o",
			params:    `{"enum":["a","b"]}`,
			wantExact: `{"enum":["a","b"]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := specWithParams("t", tt.params)
			s := &runState{
				model: tt.model,
				tools: &fakeTools{specs: []llm.ToolSpec{original}},
			}

			got := s.advertisedSpecs()
			if len(got) != 1 {
				t.Fatalf("advertisedSpecs() returned %d specs, want 1", len(got))
			}
			params := string(got[0].Function.Parameters)
			if tt.wantExact != "" {
				if params != tt.wantExact {
					t.Fatalf("Parameters = %q, want %q", params, tt.wantExact)
				}
			} else {
				for _, need := range tt.wantIn {
					if !strings.Contains(params, need) {
						t.Fatalf("Parameters %q lacks %q", params, need)
					}
				}
			}
			// The source catalog must not be mutated by projection.
			if source := string(s.tools.Specs()[0].Function.Parameters); source != tt.params {
				t.Fatalf("toolset catalog mutated: %q, want %q", source, tt.params)
			}
		})
	}
}

// TestAdvertisedSpecsProjectsAfterFilter checks that projection is applied to
// the filtered list, not the pre-filter catalog.
func TestAdvertisedSpecsProjectsAfterFilter(t *testing.T) {
	keep := specWithParams("keep", `{"enum":["a","b"]}`)
	drop := specWithParams("drop", `{"enum":["x","y"]}`)
	s := &runState{
		model: "kimi-k2",
		tools: &fakeTools{specs: []llm.ToolSpec{keep, drop}},
		opts: Options{FilterSpecs: func(specs []llm.ToolSpec) []llm.ToolSpec {
			return specs[:1]
		}},
	}

	got := s.advertisedSpecs()
	if len(got) != 1 || got[0].Function.Name != "keep" {
		t.Fatalf("filter not applied: %+v", got)
	}
	if !strings.Contains(string(got[0].Function.Parameters), `"type":"string"`) {
		t.Fatalf("projection not applied after filter: %s", got[0].Function.Parameters)
	}
}
