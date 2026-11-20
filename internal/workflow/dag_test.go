package workflow

import (
	"reflect"
	"strings"
	"testing"
)

func TestValidateDAG(t *testing.T) {
	cases := []struct {
		name    string
		steps   []Step
		want    []int
		wantErr string // substring expected in the error, empty means success
	}{
		{
			name:  "empty definition",
			steps: nil,
			want:  nil,
		},
		{
			name: "no references keeps declaration order",
			steps: []Step{
				{Name: "a", Prompt: "do a"},
				{Name: "b", Prompt: "do b"},
				{Name: "c", Prompt: "do c"},
			},
			want: []int{0, 1, 2},
		},
		{
			name: "linear backward references",
			steps: []Step{
				{Name: "a", Prompt: "scan"},
				{Name: "b", Prompt: "review {{steps.a.output}}"},
				{Name: "c", Prompt: "report {{steps.b.output}}"},
			},
			want: []int{0, 1, 2},
		},
		{
			name: "forward reference is topologically sorted",
			steps: []Step{
				{Name: "a", Prompt: "needs {{steps.c.output}}"},
				{Name: "b", Prompt: "independent"},
				{Name: "c", Prompt: "produces output"},
			},
			want: []int{1, 2, 0},
		},
		{
			name: "diamond dependencies",
			steps: []Step{
				{Name: "a", Prompt: "root"},
				{Name: "b", Prompt: "{{steps.a.output}}"},
				{Name: "c", Prompt: "{{steps.a.output}}"},
				{Name: "d", Prompt: "{{steps.b.output}} {{steps.c.output}}"},
			},
			want: []int{0, 1, 2, 3},
		},
		{
			name: "repeated reference is not a false cycle",
			steps: []Step{
				{Name: "a", Prompt: "root"},
				{Name: "b", Prompt: "{{steps.a.output}} and {{steps.a.output}}"},
			},
			want: []int{0, 1},
		},
		{
			name: "whitespace in token and args reference",
			steps: []Step{
				{Name: "a", Prompt: "root"},
				{Name: "b", Tool: "write", Args: `{"body":"{{ steps.a.output }}"}`},
			},
			want: []int{0, 1},
		},
		{
			name: "two step cycle",
			steps: []Step{
				{Name: "a", Prompt: "{{steps.b.output}}"},
				{Name: "b", Prompt: "{{steps.a.output}}"},
			},
			wantErr: "cyclic step dependency",
		},
		{
			name: "self reference is a cycle",
			steps: []Step{
				{Name: "a", Prompt: "{{steps.a.output}}"},
			},
			wantErr: "cyclic step dependency",
		},
		{
			name: "cycle with a dependent step is reported together",
			steps: []Step{
				{Name: "a", Prompt: "{{steps.b.output}}"},
				{Name: "b", Prompt: "{{steps.a.output}}"},
				{Name: "c", Prompt: "{{steps.a.output}}"},
			},
			wantErr: `"a", "b", "c"`,
		},
		{
			name: "missing dependency",
			steps: []Step{
				{Name: "a", Prompt: "review {{steps.ghost.output}}"},
			},
			wantErr: `depends on undefined step "ghost"`,
		},
		{
			name: "missing dependency in args",
			steps: []Step{
				{Name: "a", Tool: "write", Args: `{"body":"{{steps.later.output}}"}`},
			},
			wantErr: `depends on undefined step "later"`,
		},
		{
			name: "CJK normal DAG",
			steps: []Step{
				{Name: "扫描", Prompt: "列出改动"},
				{Name: "复核", Prompt: "复核 {{steps.扫描.output}}"},
				{Name: "报告", Prompt: "汇总 {{steps.复核.output}}"},
			},
			want: []int{0, 1, 2},
		},
		{
			name: "CJK cycle names are reported",
			steps: []Step{
				{Name: "甲", Prompt: "{{steps.乙.output}}"},
				{Name: "乙", Prompt: "{{steps.甲.output}}"},
			},
			wantErr: "cyclic step dependency",
		},
		{
			name: "CJK missing dependency name is reported",
			steps: []Step{
				{Name: "校对", Prompt: "{{steps.不存在.output}}"},
			},
			wantErr: "不存在",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			order, err := ValidateDAG(tc.steps)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("ValidateDAG() = %v, want an error containing %q", order, tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %q, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateDAG: %v", err)
			}
			if !reflect.DeepEqual(order, tc.want) {
				t.Fatalf("order = %v, want %v", order, tc.want)
			}
		})
	}
}

// The capability is wired into definition validation: a cycle or a dangling
// step reference is rejected before any model or tool is invoked.
func TestValidateDAGWiredIntoParseDefinition(t *testing.T) {
	good := `{"steps":[
		{"name":"scan","prompt":"scan"},
		{"name":"review","prompt":"review {{steps.scan.output}}"}
	]}`
	if _, err := ParseDefinition([]byte(good)); err != nil {
		t.Fatalf("valid backward reference should parse: %v", err)
	}

	bad := []string{
		`{"steps":[
			{"name":"a","prompt":"{{steps.b.output}}"},
			{"name":"b","prompt":"{{steps.a.output}}"}
		]}`,
		`{"steps":[{"name":"a","prompt":"{{steps.missing.output}}"}]}`,
		`{"steps":[{"name":"甲","prompt":"{{steps.乙.output}}"},{"name":"乙","prompt":"{{steps.甲.output}}"}]}`,
	}
	for _, body := range bad {
		if _, err := ParseDefinition([]byte(body)); err == nil {
			t.Fatalf("definition with a broken dependency graph should be rejected: %s", body)
		}
	}
}
