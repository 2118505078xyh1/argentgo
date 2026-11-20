package security

import (
	"strings"
	"testing"
)

// TestFindFileEditingCommandChecksEveryRedirect covers a redirection bypass:
// several `>` operators can share one shell word (e.g. `>/etc/passwd>/tmp/x`).
// The old LastIndex code only inspected the final target, so a scratch target
// at the end hid an earlier write to a real file. Each redirection in the word
// must be checked independently.
func TestFindFileEditingCommandChecksEveryRedirect(t *testing.T) {
	cases := []struct {
		name    string
		command string
		want    string // substring expected in the offending token; "" = no match
	}{
		{
			name:    "real file before scratch target is still a write",
			command: "echo hi >/etc/passwd>/tmp/scratch.txt",
			want:    "/etc/passwd",
		},
		{
			name:    "real file between two scratch targets",
			command: "echo hi >/tmp/a>/etc/passwd>/tmp/b",
			want:    "/etc/passwd",
		},
		{
			name:    "append redirect before scratch target",
			command: "echo hi >>/etc/passwd>/tmp/scratch.txt",
			want:    "/etc/passwd",
		},
		{
			name:    "all scratch targets stay clear",
			command: "echo hi >/tmp/a>/tmp/b",
			want:    "",
		},
		{
			name:    "fd duplication before a scratch target stays clear",
			command: "echo hi 2>&1>/tmp/scratch.txt",
			want:    "",
		},
		{
			name:    "separate redirection fields still checked",
			command: "echo hi >/etc/passwd >/tmp/scratch.txt",
			want:    "/etc/passwd",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FindFileEditingCommand(tc.command)
			if tc.want == "" {
				if got != "" {
					t.Fatalf("FindFileEditingCommand(%q) = %q, want no match", tc.command, got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("FindFileEditingCommand(%q) = %q, want it to contain %q", tc.command, got, tc.want)
			}
		})
	}
}
