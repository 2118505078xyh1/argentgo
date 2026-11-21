package cli

import (
	"io"
	"testing"
)

// TestParseAtValueStorage 验证 --at 的值真正流入 Options.At，且不带 --at 时为空。
func TestParseAtValueStorage(t *testing.T) {
	env := func(string) string { return "" }
	cases := []struct {
		name       string
		args       []string
		wantAt     string
		wantPrompt []string
	}{
		{
			name:       "session with at",
			args:       []string{"--session", "s1", "--at", "e2", "go on"},
			wantAt:     "e2",
			wantPrompt: []string{"go on"},
		},
		{
			name:       "at with equals form",
			args:       []string{"--session=s1", "--at=e9"},
			wantAt:     "e9",
			wantPrompt: nil,
		},
		{
			name:       "no at keeps empty",
			args:       []string{"--session", "s1", "go on"},
			wantAt:     "",
			wantPrompt: []string{"go on"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts, prompt, err := parseArgs(tc.args, env, io.Discard)
			if err != nil {
				t.Fatalf("parseArgs: %v", err)
			}
			if opts.At != tc.wantAt {
				t.Fatalf("At = %q, want %q", opts.At, tc.wantAt)
			}
			if len(prompt) != len(tc.wantPrompt) {
				t.Fatalf("prompt = %q, want %q", prompt, tc.wantPrompt)
			}
			for i := range prompt {
				if prompt[i] != tc.wantPrompt[i] {
					t.Fatalf("prompt = %q, want %q", prompt, tc.wantPrompt)
				}
			}
		})
	}
}
