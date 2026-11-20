package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestValidateParamDefaultsAcceptsWellTypedDefaults pins the accepted Go
// kinds for each input_type. The set mirrors stringifyDefault so a value that
// passes validation can always be rendered by the resolver.
func TestValidateParamDefaultsAcceptsWellTypedDefaults(t *testing.T) {
	cases := []struct {
		name  string
		param WorkflowParam
	}{
		{"string", WorkflowParam{Key: "name", InputType: ParamTypeString, Requirement: ParamOptional, Default: "Paris"}},
		{"number float64", WorkflowParam{Key: "n", InputType: ParamTypeNumber, Requirement: ParamOptional, Default: float64(3)}},
		{"number int", WorkflowParam{Key: "n", InputType: ParamTypeNumber, Requirement: ParamOptional, Default: 3}},
		{"number json.Number", WorkflowParam{Key: "n", InputType: ParamTypeNumber, Requirement: ParamOptional, Default: json.Number("3")}},
		{"boolean", WorkflowParam{Key: "b", InputType: ParamTypeBoolean, Requirement: ParamOptional, Default: true}},
		{"date", WorkflowParam{Key: "d", InputType: ParamTypeDate, Requirement: ParamOptional, Default: "2026-01-01"}},
		{"select in options", WorkflowParam{Key: "s", InputType: ParamTypeSelect, Requirement: ParamOptional, Default: "fast", Options: []string{"fast", "slow"}}},
		{"file without default", WorkflowParam{Key: "f", InputType: ParamTypeFile, Requirement: ParamOptional}},
		{"no default", WorkflowParam{Key: "x", InputType: ParamTypeString, Requirement: ParamRequired}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			def := Definition{Parameters: []WorkflowParam{tc.param}}
			if err := ValidateParamDefaults(def); err != nil {
				t.Fatalf("ValidateParamDefaults: %v", err)
			}
		})
	}
}

// TestValidateParamDefaultsRejectsMismatchedDefaults covers the declaration
// bugs the resolver used to accept silently: a default of the wrong Go kind,
// a malformed date, and a select default outside its options.
func TestValidateParamDefaultsRejectsMismatchedDefaults(t *testing.T) {
	cases := []struct {
		name  string
		param WorkflowParam
		want  string
	}{
		{
			name:  "string wants a number",
			param: WorkflowParam{Key: "name", InputType: ParamTypeString, Requirement: ParamOptional, Default: 3},
			want:  "default does not match string input",
		},
		{
			name:  "number wants a numeric value",
			param: WorkflowParam{Key: "count", InputType: ParamTypeNumber, Requirement: ParamOptional, Default: "3"},
			want:  "default does not match number input",
		},
		{
			name:  "boolean wants a bool",
			param: WorkflowParam{Key: "flag", InputType: ParamTypeBoolean, Requirement: ParamOptional, Default: "true"},
			want:  "default does not match boolean input",
		},
		{
			name:  "date wants a YYYY-MM-DD string",
			param: WorkflowParam{Key: "when", InputType: ParamTypeDate, Requirement: ParamOptional, Default: "soon"},
			want:  "default does not match date input",
		},
		{
			name:  "date is not a string",
			param: WorkflowParam{Key: "when", InputType: ParamTypeDate, Requirement: ParamOptional, Default: 20260101},
			want:  "default does not match date input",
		},
		{
			name:  "select default outside options",
			param: WorkflowParam{Key: "mode", InputType: ParamTypeSelect, Requirement: ParamOptional, Default: "turbo", Options: []string{"fast", "slow"}},
			want:  "default does not match select input",
		},
		{
			name:  "select default is not a string",
			param: WorkflowParam{Key: "mode", InputType: ParamTypeSelect, Requirement: ParamOptional, Default: 1, Options: []string{"1"}},
			want:  "default does not match select input",
		},
		{
			name:  "file cannot carry a default",
			param: WorkflowParam{Key: "path", InputType: ParamTypeFile, Requirement: ParamOptional, Default: "/tmp/x"},
			want:  "default does not match file input",
		},
		{
			name:  "unknown input type",
			param: WorkflowParam{Key: "x", InputType: "uuid", Requirement: ParamOptional, Default: "abc"},
			want:  "default does not match uuid input",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			def := Definition{Parameters: []WorkflowParam{tc.param}}
			err := ValidateParamDefaults(def)
			if err == nil {
				t.Fatalf("%s: a mismatched default should be rejected", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s: error = %q, want it to contain %q", tc.name, err.Error(), tc.want)
			}
			if !strings.Contains(err.Error(), "("+tc.param.Key+")") {
				t.Fatalf("%s: error should name the parameter: %q", tc.name, err.Error())
			}
		})
	}
}

// TestDefinitionValidateWiresParamDefaultCheck proves the new check is reachable
// through the public entry points, not just when called directly. Both
// ParseDefinition (which validates) and Definition.Validate must reject a
// well-formed declaration whose default contradicts its input_type.
func TestDefinitionValidateWiresParamDefaultCheck(t *testing.T) {
	bad := `{"name":"review","parameters":[
		{"key":"repo","input_type":"string","requirement":"required"},
		{"key":"count","input_type":"number","requirement":"optional","default":"not-a-number"}
	],"steps":[{"name":"a","prompt":"count {{count}} in {{repo}}"}]}`
	if _, err := ParseDefinition([]byte(bad)); err == nil || !strings.Contains(err.Error(), "default does not match number input") {
		t.Fatalf("ParseDefinition did not surface the default check: %v", err)
	}

	good := `{"name":"review","parameters":[
		{"key":"repo","input_type":"string","requirement":"required"},
		{"key":"count","input_type":"number","requirement":"optional","default":3},
		{"key":"flag","input_type":"boolean","requirement":"optional","default":true},
		{"key":"when","input_type":"date","requirement":"optional","default":"2026-01-01"},
		{"key":"mode","input_type":"select","requirement":"optional","default":"fast","options":["fast","slow"]}
	],"steps":[{"name":"a","prompt":"{{repo}} {{count}} {{flag}} {{when}} {{mode}}"}]}`
	def := mustDefinition(t, good)
	if err := def.Validate(); err != nil {
		t.Fatalf("a well-typed declaration should validate: %v", err)
	}

	// The executor shares the same validation gate, so a malformed default is
	// rejected before any model or tool is invoked.
	exec := &Executor{Prompter: prompterFunc(func(context.Context, string) (string, error) {
		t.Fatal("the prompter must not run for a definition with a bad default")
		return "", nil
	})}
	badDef := &Definition{
		Name: "review",
		Parameters: []WorkflowParam{
			{Key: "repo", InputType: ParamTypeString, Requirement: ParamRequired},
			{Key: "count", InputType: ParamTypeNumber, Requirement: ParamOptional, Default: "not-a-number"},
		},
		Steps: []Step{{Name: "a", Prompt: "count {{count}} in {{repo}}"}},
	}
	if _, err := exec.RunWithParams(context.Background(), badDef, "", map[string]string{"repo": "acme/api"}); err == nil {
		t.Fatal("RunWithParams should reject a malformed default")
	}
}

// TestValidateParamDefaultsErrorsAreSecretSafe guards the rule that validation
// errors may name a key and a type but never echo the declared value.
func TestValidateParamDefaultsErrorsAreSecretSafe(t *testing.T) {
	const canary = "canary-secret-242"
	def := Definition{Parameters: []WorkflowParam{
		{Key: "count", InputType: ParamTypeNumber, Requirement: ParamOptional, Default: canary},
		{Key: "mode", InputType: ParamTypeSelect, Requirement: ParamOptional, Default: canary, Options: []string{"fast"}},
		{Key: "when", InputType: ParamTypeDate, Requirement: ParamOptional, Default: canary},
	}}
	err := ValidateParamDefaults(def)
	if err == nil {
		t.Fatal("expected a validation error")
	}
	if strings.Contains(err.Error(), canary) {
		t.Fatalf("error leaked the secret value: %q", err.Error())
	}
}
