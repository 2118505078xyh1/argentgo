package agent

import (
	"errors"
	"strings"
	"testing"
)

func TestParseCodeModeScript(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantN   int
		wantErr bool
	}{
		{"array form", `[{"tool":"read","args":{"path":"a.go"}}]`, 1, false},
		{"wrapper form", `{"steps":[{"tool":"read","args":{}},{"tool":"grep","args":{}}]}`, 2, false},
		{"empty", ``, 0, true},
		{"no steps", `{"steps":[]}`, 0, true},
		{"missing tool", `[{"args":{}}]`, 0, true},
		{"bad json", `[{"tool":`, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			steps, err := parseCodeModeScript(tc.raw)
			if (err != nil) != tc.wantErr {
				t.Fatalf("parse %q: err=%v, wantErr=%v", tc.raw, err, tc.wantErr)
			}
			if !tc.wantErr && len(steps) != tc.wantN {
				t.Fatalf("steps = %d, want %d", len(steps), tc.wantN)
			}
		})
	}
}

func TestParseCodeModeScriptStepLimit(t *testing.T) {
	var b strings.Builder
	b.WriteString("[")
	for i := 0; i < codeModeMaxSteps+1; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"tool":"read","args":{}}`)
	}
	b.WriteString("]")
	if _, err := parseCodeModeScript(b.String()); !errors.Is(err, ErrCodeMode) {
		t.Fatalf("超步数应报 ErrCodeMode, got %v", err)
	}
}

func TestInterpolateCodeModeRefs(t *testing.T) {
	steps := []stepOutput{
		{Output: "first-result", As: "one", Done: true},
		{Output: "second-result", As: "two", Done: true},
	}
	cases := map[string]string{
		`{{steps.0.output}}`:               "first-result",
		`{{vars.two}}`:                     "second-result",
		`prefix-{{steps.1.output}}-suffix`: "prefix-second-result-suffix",
		`{{steps.9.output}}`:               "{{steps.9.output}}", // 越界原样保留
		`{{vars.missing}}`:                 "{{vars.missing}}",
		`no placeholder`:                   "no placeholder",
	}
	for in, want := range cases {
		if got := interpolateCodeMode(in, steps); got != want {
			t.Errorf("interpolate(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestInterpolateArgsRecursive(t *testing.T) {
	steps := []stepOutput{{Output: "abc", As: "x", Done: true}}
	in := map[string]any{
		"path":   "{{vars.x}}.go",
		"nested": []any{"{{steps.0.output}}", 42},
	}
	out := interpolateArgs(in, steps).(map[string]any)
	if out["path"] != "abc.go" {
		t.Fatalf("path = %v", out["path"])
	}
	arr := out["nested"].([]any)
	if arr[0] != "abc" || arr[1] != 42 {
		t.Fatalf("nested = %v", arr)
	}
}

func TestTruncateCodeModeValue(t *testing.T) {
	s := strings.Repeat("x", codeModeMaxStepOutput+100)
	got := truncateCodeModeValue(s)
	if len(got) <= codeModeMaxStepOutput {
		t.Fatalf("插值未被截断: %d", len(got))
	}
	if !strings.Contains(got, "truncated") {
		t.Fatal("截断应有提示")
	}
}
