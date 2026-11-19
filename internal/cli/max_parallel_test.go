package cli

import (
	"io"
	"testing"
)

func TestParseMaxParallel(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		env   map[string]string
		want  int
		fails bool
	}{
		{name: "unset stays zero for config fallback", want: 0},
		{name: "flag", args: []string{"--max-parallel", "8"}, want: 8},
		{name: "flag equals form", args: []string{"--max-parallel=1"}, want: 1},
		{name: "environment", env: map[string]string{"ARGENT_MAX_PARALLEL": "5"}, want: 5},
		{
			name: "flag beats environment",
			args: []string{"--max-parallel", "2"},
			env:  map[string]string{"ARGENT_MAX_PARALLEL": "5"},
			want: 2,
		},
		{name: "camel case override", args: []string{"-c", "maxParallel=6"}, want: 6},
		{name: "kebab case override", args: []string{"-c", "max-parallel=3"}, want: 3},
		{
			name: "environment beats override",
			args: []string{"-c", "maxParallel=6"},
			env:  map[string]string{"ARGENT_MAX_PARALLEL": "4"},
			want: 4,
		},
		{name: "negative rejected", args: []string{"--max-parallel", "-1"}, fails: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdio, _, _ := newTestIO("", tc.env)
			opts, _, err := parseArgs(tc.args, stdio.Env, io.Discard)
			if tc.fails {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseArgs: %v", err)
			}
			if opts.MaxParallel != tc.want {
				t.Fatalf("MaxParallel = %d, want %d", opts.MaxParallel, tc.want)
			}
		})
	}
}
