package main

import "testing"

// Only the leading `gui` subcommand may divert the CLI into the native
// frontend. Scanning every argument hijacks prompts and flag values.
func TestIsGUIRequestOnlyMatchesLeadingGUISubcommand(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"no args", nil, false},
		{"gui subcommand", []string{"gui"}, true},
		{"gui subcommand with flags", []string{"gui", "--model", "m1"}, true},
		{"prompt containing gui", []string{"fix", "gui", "bug"}, false},
		{"flag value named gui", []string{"--model", "gui", "hello"}, false},
		{"trailing gui prompt word", []string{"hello", "gui"}, false},
		{"gui flag", []string{"--gui"}, false},
		{"gui flag with prompt", []string{"--gui", "hello"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isGUIRequest(tc.args); got != tc.want {
				t.Fatalf("isGUIRequest(%q) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}
