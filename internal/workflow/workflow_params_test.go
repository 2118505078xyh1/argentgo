package workflow

import (
	"fmt"
	"strings"
	"testing"
)

func paramDefinition(steps []Step, params ...WorkflowParam) Definition {
	return Definition{Name: "params", Steps: steps, Parameters: params}
}

func TestExtractPlaceholdersDeduplicatesAndSorts(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []string
	}{
		{"none", "plain text", nil},
		{"trims and dedups", "{{ b }} {{a}} {{b}}", []string{"a", "b"}},
		{"keeps builtin names", "{{input}} {{steps.scan.output}}", []string{"input", "steps.scan.output"}},
		{"ignores unterminated", "{{unterminated", nil},
		{"ignores empty", "{{}} {{ok}}", []string{"ok"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractPlaceholders(tc.text)
			if len(got) != len(tc.want) {
				t.Fatalf("ExtractPlaceholders(%q) = %q, want %q", tc.text, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("ExtractPlaceholders(%q) = %q, want %q", tc.text, got, tc.want)
				}
			}
		})
	}
}

func TestExtractPlaceholdersMatchesSubstituteSyntax(t *testing.T) {
	text := "{{ input }} / {{steps.scan.output}} / {{again}} / {{again}} / {{unterminated"
	want := []string{"again", "input", "steps.scan.output"}
	got := ExtractPlaceholders(text)
	if len(got) != len(want) {
		t.Fatalf("names = %q, want %q", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("names = %q, want %q", got, want)
		}
	}
	// Substitute 解析的正是同一种标记拼写。
	out := Substitute(text, "I", map[string]string{"scan": "S"})
	wantOut := "I / S / {{again}} / {{again}} / {{unterminated"
	if out != wantOut {
		t.Fatalf("Substitute(%q) = %q, want %q", text, out, wantOut)
	}
}

func TestValidateParametersAcceptsValidDeclarations(t *testing.T) {
	def := paramDefinition(
		[]Step{
			{Name: "a", Prompt: "hello {{name}}", Args: `{"count":{{count}},"flag":{{flag}}}`},
			{Name: "b", Prompt: "on {{when}} via {{mode}}"},
		},
		WorkflowParam{Key: "name", InputType: ParamTypeString, Requirement: ParamRequired},
		WorkflowParam{Key: "count", InputType: ParamTypeNumber, Requirement: ParamOptional, Default: float64(3)},
		WorkflowParam{Key: "flag", InputType: ParamTypeBoolean, Requirement: ParamUserPrompt, Default: true},
		WorkflowParam{Key: "when", InputType: ParamTypeDate, Requirement: ParamOptional, Default: "2026-01-01"},
		WorkflowParam{Key: "mode", InputType: ParamTypeSelect, Requirement: ParamOptional, Default: "fast", Options: []string{"fast", "slow"}},
	)
	if err := ValidateParameters(def); err != nil {
		t.Fatalf("ValidateParameters: %v", err)
	}
	if err := def.Validate(); err != nil {
		t.Fatalf("Definition.Validate: %v", err)
	}

	// 无参数的 JSON 路径仍可用，且格式良好的
	// 无参数的 JSON 路径仍可用，且格式良好的参数化文档能通过 ParseDefinition 解析。
	body := `{"name":"params","parameters":[
		{"key":"name","input_type":"string","requirement":"required"},
		{"key":"count","input_type":"number","requirement":"optional","default":3},
		{"key":"flag","input_type":"boolean","requirement":"user_prompt","default":true},
		{"key":"mode","input_type":"select","requirement":"optional","default":"fast","options":["fast","slow"]}
	],"steps":[{"name":"a","prompt":"hi {{name}} {{count}} {{flag}} {{mode}}"}]}`
	parsed := mustDefinition(t, body)
	if len(parsed.Parameters) != 4 {
		t.Fatalf("parsed parameters = %d, want 4", len(parsed.Parameters))
	}
	if got, ok := parsed.Parameters[1].Default.(float64); !ok || got != 3 {
		t.Fatalf("number default = %#v", parsed.Parameters[1].Default)
	}
}

func TestValidateParametersRejectsUndeclaredPlaceholder(t *testing.T) {
	def := paramDefinition(
		[]Step{{Name: "a", Prompt: "hi {{name}} and {{ghost}}"}},
		WorkflowParam{Key: "name", InputType: ParamTypeString, Requirement: ParamRequired},
	)
	err := ValidateParameters(def)
	if err == nil {
		t.Fatal("an undeclared placeholder should be rejected")
	}
	if !strings.Contains(err.Error(), `placeholder "ghost"`) {
		t.Fatalf("error = %q", err.Error())
	}
}

func TestValidateParametersRejectsUnusedDeclaration(t *testing.T) {
	def := paramDefinition(
		[]Step{{Name: "a", Prompt: "hi {{name}}"}},
		WorkflowParam{Key: "name", InputType: ParamTypeString, Requirement: ParamRequired},
		WorkflowParam{Key: "extra", InputType: ParamTypeString, Requirement: ParamOptional},
	)
	err := ValidateParameters(def)
	if err == nil {
		t.Fatal("an unused declaration should be rejected")
	}
	if !strings.Contains(err.Error(), `parameter "extra"`) {
		t.Fatalf("error = %q", err.Error())
	}
}

func TestValidateParametersRejectsInvalidFields(t *testing.T) {
	dup := WorkflowParam{Key: "name", InputType: ParamTypeString, Requirement: ParamOptional}
	cases := []struct {
		name string
		def  Definition
		want string
	}{
		{
			name: "empty key",
			def:  paramDefinition([]Step{{Name: "a", Prompt: "hi {{name}}"}}, WorkflowParam{InputType: ParamTypeString, Requirement: ParamOptional}),
			want: "parameters[0].key is required",
		},
		{
			name: "reserved key",
			def:  paramDefinition([]Step{{Name: "a", Prompt: "hi {{input}}"}}, WorkflowParam{Key: "input", InputType: ParamTypeString, Requirement: ParamOptional}),
			want: "parameters[0].key \"input\" is reserved",
		},
		{
			name: "duplicate key",
			def:  paramDefinition([]Step{{Name: "a", Prompt: "hi {{name}}"}}, dup, dup),
			want: "is duplicated",
		},
		{
			name: "bad type",
			def:  paramDefinition([]Step{{Name: "a", Prompt: "hi {{name}}"}}, WorkflowParam{Key: "name", InputType: "uuid", Requirement: ParamOptional}),
			want: "parameters[0].input_type is invalid",
		},
		{
			name: "bad requirement",
			def:  paramDefinition([]Step{{Name: "a", Prompt: "hi {{name}}"}}, WorkflowParam{Key: "name", InputType: ParamTypeString, Requirement: "maybe"}),
			want: "parameters[0].requirement is invalid",
		},
		{
			name: "select without options",
			def:  paramDefinition([]Step{{Name: "a", Prompt: "hi {{name}}"}}, WorkflowParam{Key: "name", InputType: ParamTypeSelect, Requirement: ParamOptional}),
			want: "needs options",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateParameters(tc.def)
			if err == nil {
				t.Fatalf("%s should be rejected", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s: error = %q, want it to contain %q", tc.name, err.Error(), tc.want)
			}
		})
	}
}

func TestValidateParametersRejectsDisallowedDefaults(t *testing.T) {
	cases := []struct {
		name  string
		param WorkflowParam
		want  string
	}{
		{
			name:  "file with default",
			param: WorkflowParam{Key: "path", InputType: ParamTypeFile, Requirement: ParamOptional, Default: "/etc/hosts"},
			want:  "file input cannot have a default",
		},
		{
			name:  "required with default",
			param: WorkflowParam{Key: "token", InputType: ParamTypeString, Requirement: ParamRequired, Default: "abc"},
			want:  "required input cannot have a default",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			def := paramDefinition([]Step{{Name: "a", Prompt: "use {{" + tc.param.Key + "}}"}}, tc.param)
			err := ValidateParameters(def)
			if err == nil {
				t.Fatalf("%s should be rejected", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s: error = %q, want it to contain %q", tc.name, err.Error(), tc.want)
			}
		})
	}
}

func TestValidateParametersAllowsOptionalDefaultButNotRequiredOne(t *testing.T) {
	// 可选参数（以及 user_prompt 参数）可以带默认值。
	withDefault := paramDefinition([]Step{{Name: "a", Prompt: "{{city}}"}},
		WorkflowParam{Key: "city", InputType: ParamTypeString, Requirement: ParamOptional, Default: "Paris"})
	if err := ValidateParameters(withDefault); err != nil {
		t.Fatalf("optional with default: %v", err)
	}
	// 必需参数不可以。
	required := paramDefinition([]Step{{Name: "a", Prompt: "{{city}}"}},
		WorkflowParam{Key: "city", InputType: ParamTypeString, Requirement: ParamRequired})
	if err := ValidateParameters(required); err != nil {
		t.Fatalf("required without default: %v", err)
	}
}

func TestValidateParametersEnforcesCaps(t *testing.T) {
	// 参数过多。
	var steps []Step
	params := make([]WorkflowParam, 0, maxWorkflowParams+1)
	for i := 0; i <= maxWorkflowParams; i++ {
		key := fmt.Sprintf("p%d", i)
		steps = append(steps, Step{Name: fmt.Sprintf("s%d", i), Prompt: "{{" + key + "}}"})
		params = append(params, WorkflowParam{Key: key, InputType: ParamTypeString, Requirement: ParamOptional})
	}
	if err := ValidateParameters(paramDefinition(steps, params...)); err == nil || !strings.Contains(err.Error(), "too many parameters") {
		t.Fatalf("parameter count cap: err = %v", err)
	}

	// select 选项总数过多。
	options := make([]string, maxTotalSelectOptions+1)
	for i := range options {
		options[i] = fmt.Sprintf("o%d", i)
	}
	def := paramDefinition([]Step{{Name: "a", Prompt: "{{mode}}"}},
		WorkflowParam{Key: "mode", InputType: ParamTypeSelect, Requirement: ParamOptional, Options: options})
	if err := ValidateParameters(def); err == nil || !strings.Contains(err.Error(), "select options exceed") {
		t.Fatalf("select option cap: err = %v", err)
	}

	// 序列化参数过大。
	def = paramDefinition([]Step{{Name: "a", Prompt: "{{blob}}"}},
		WorkflowParam{Key: "blob", InputType: ParamTypeString, Requirement: ParamOptional, Default: strings.Repeat("x", maxSerializedParamsBytes+1)})
	if err := ValidateParameters(def); err == nil || !strings.Contains(err.Error(), "when serialized") {
		t.Fatalf("serialized size cap: err = %v", err)
	}
}

func TestValidateParametersErrorsAreSecretSafe(t *testing.T) {
	const canary = "canary-secret-242"
	cases := []struct {
		name string
		def  Definition
	}{
		{
			name: "required default with canary",
			def: paramDefinition([]Step{{Name: "a", Prompt: "{{token}}"}},
				WorkflowParam{Key: "token", InputType: ParamTypeString, Requirement: ParamRequired, Default: canary}),
		},
		{
			name: "file default with canary",
			def: paramDefinition([]Step{{Name: "a", Prompt: "{{path}}"}},
				WorkflowParam{Key: "path", InputType: ParamTypeFile, Requirement: ParamOptional, Default: canary}),
		},
		{
			name: "invalid type with canary in options",
			def: paramDefinition([]Step{{Name: "a", Prompt: "{{mode}}"}},
				WorkflowParam{Key: "mode", InputType: "bogus", Requirement: ParamOptional, Options: []string{canary}}),
		},
		{
			name: "oversized default with canary",
			def: paramDefinition([]Step{{Name: "a", Prompt: "{{blob}}"}},
				WorkflowParam{Key: "blob", InputType: ParamTypeString, Requirement: ParamOptional, Default: strings.Repeat("x", maxSerializedParamsBytes) + canary}),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateParameters(tc.def)
			if err == nil {
				t.Fatal("expected a validation error")
			}
			if strings.Contains(err.Error(), canary) {
				t.Fatalf("error leaked the secret value: %q", err.Error())
			}
		})
	}
}

func TestResolveParametersFillsAndCoerces(t *testing.T) {
	def := paramDefinition(nil,
		WorkflowParam{Key: "name", InputType: ParamTypeString, Requirement: ParamRequired},
		WorkflowParam{Key: "count", InputType: ParamTypeNumber, Requirement: ParamOptional, Default: float64(3)},
		WorkflowParam{Key: "flag", InputType: ParamTypeBoolean, Requirement: ParamUserPrompt, Default: true},
		WorkflowParam{Key: "when", InputType: ParamTypeDate, Requirement: ParamOptional, Default: "2026-01-01"},
		WorkflowParam{Key: "mode", InputType: ParamTypeSelect, Requirement: ParamOptional, Default: "fast", Options: []string{"fast", "slow"}},
		WorkflowParam{Key: "skip", InputType: ParamTypeString, Requirement: ParamOptional},
	)
	got, err := ResolveParameters(def, map[string]string{"name": "Ada"})
	if err != nil {
		t.Fatalf("ResolveParameters: %v", err)
	}
	want := map[string]string{
		"name":  "Ada",
		"count": "3",
		"flag":  "true",
		"when":  "2026-01-01",
		"mode":  "fast",
	}
	if len(got) != len(want) {
		t.Fatalf("resolved = %#v, want %#v", got, want)
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("resolved[%q] = %q, want %q", key, got[key], value)
		}
	}
	if _, ok := got["skip"]; ok {
		t.Fatal("an optional parameter without a value should be absent")
	}

	// 序列化参数过大。
	got, err = ResolveParameters(def, map[string]string{"name": "Ada", "count": "9"})
	if err != nil {
		t.Fatalf("ResolveParameters (override): %v", err)
	}
	if got["count"] != "9" {
		t.Fatalf("provided value should win, got %q", got["count"])
	}
}

func TestResolveParametersReportsMissingRequired(t *testing.T) {
	def := paramDefinition(nil,
		WorkflowParam{Key: "token", InputType: ParamTypeString, Requirement: ParamRequired})
	for _, provided := range []map[string]string{nil, {"token": ""}} {
		_, err := ResolveParameters(def, provided)
		if err == nil {
			t.Fatalf("provided = %#v: a missing required value should error", provided)
		}
		if !strings.Contains(err.Error(), `parameter "token" is required`) {
			t.Fatalf("error = %q", err.Error())
		}
	}
}

func TestResolveParametersCapsValues(t *testing.T) {
	const canary = "canary-secret-242"
	oversized := strings.Repeat("y", maxParamValueBytes+1) + canary
	def := paramDefinition(nil,
		WorkflowParam{Key: "blob", InputType: ParamTypeString, Requirement: ParamOptional, Default: oversized})
	for _, tc := range []struct {
		name     string
		provided map[string]string
	}{
		{"from default", nil},
		{"from provided", map[string]string{"blob": oversized}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ResolveParameters(def, tc.provided)
			if err == nil {
				t.Fatal("an oversized value should error")
			}
			if !strings.Contains(err.Error(), "value exceeds") {
				t.Fatalf("error = %q", err.Error())
			}
			if strings.Contains(err.Error(), canary) {
				t.Fatalf("error leaked the secret value: %q", err.Error())
			}
		})
	}
}

func TestParameterlessWorkflowsKeepWorking(t *testing.T) {
	// 提供的值优先于默认值。
	// 现有文档使用看起来自定义的标记为模型保留内容；在没有声明参数时它们必须仍然有效。
	def := mustDefinition(t, `{"steps":[
		{"name":"a","prompt":"use {{legacy_token}} and {{input}}"}
	]}`)
	if len(def.Parameters) != 0 {
		t.Fatalf("parameters = %#v", def.Parameters)
	}
	if err := def.Validate(); err != nil {
		t.Fatalf("legacy definition should validate: %v", err)
	}
	if err := ValidateParameters(Definition{Steps: []Step{{Prompt: "{{anything}}"}}}); err != nil {
		t.Fatalf("no declarations means no new checks: %v", err)
	}
	// 现有模板仍像以前一样渲染。
	got := Substitute("review {{steps.scan.output}} for {{input}}", "diff", map[string]string{"scan": "files"})
	if got != "review files for diff" {
		t.Fatalf("Substitute = %q", got)
	}
}
