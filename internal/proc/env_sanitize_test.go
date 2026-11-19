package proc

import (
	"runtime"
	"strings"
	"testing"
)

// envHasValue reports whether any "K=V" entry in env has value as its value.
// Tests set unique sentinel values with t.Setenv and assert on those values,
// which keeps them deterministic regardless of the rest of the parent env.
func envHasValue(env []string, value string) bool {
	for _, entry := range env {
		_, v, _ := strings.Cut(entry, "=")
		if v == value {
			return true
		}
	}
	return false
}

func envHasEntry(env []string, want string) bool {
	for _, entry := range env {
		if entry == want {
			return true
		}
	}
	return false
}

func TestSanitizeEnv(t *testing.T) {
	const (
		sessionVal   = "sentinel-ARGENT-SESSION-ID-9f3a"
		runVal       = "sentinel-ARGENT-RUN-ID-7c1b"
		parentVal    = "sentinel-ARGENT-PARENT-X-2d8e"
		subParentVal = "sentinel-SUBAGENT-PARENT-X-4a6f"
		normalVal    = "sentinel-normal-5b2c"
		caseVal      = "sentinel-mixed-case-8e1d"
		extraVal     = "sentinel-extra-wins-1c7e"
		equalsVal    = "left=middle=right"
		sessionEqVal = "session=eq=secret"
	)

	// The mixed-case private key is filtered on Windows (case-insensitive) and
	// kept elsewhere (case-sensitive).
	mixedCaseKey := "argent_session_id"
	mixedCaseWantGone := []string{caseVal}
	mixedCaseWantKept := []string(nil)
	if runtime.GOOS != "windows" {
		mixedCaseWantGone = nil
		mixedCaseWantKept = []string{mixedCaseKey + "=" + caseVal}
	}

	tests := []struct {
		name     string
		setEnv   map[string]string
		extra    []string
		wantGone []string // sentinel values that must not survive
		wantKept []string // exact "K=V" entries that must survive
	}{
		{
			name:     "private exact vars are filtered",
			setEnv:   map[string]string{"ARGENT_SESSION_ID": sessionVal, "ARGENT_RUN_ID": runVal},
			wantGone: []string{sessionVal, runVal},
		},
		{
			name:     "a normal var passes through",
			setEnv:   map[string]string{"NORMAL_ARGENT_VAR": normalVal},
			wantKept: []string{"NORMAL_ARGENT_VAR=" + normalVal},
		},
		{
			name:     "extra wins over a filtered parent var",
			setEnv:   map[string]string{"ARGENT_SESSION_ID": sessionVal},
			extra:    []string{"ARGENT_SESSION_ID=" + extraVal},
			wantGone: []string{sessionVal},
			wantKept: []string{"ARGENT_SESSION_ID=" + extraVal},
		},
		{
			name:  "extra entries are never filtered",
			extra: []string{"ARGENT_PARENT_FORCED=yes-force", "SUBAGENT_PARENT_X=extra-sub"},
			wantKept: []string{
				"ARGENT_PARENT_FORCED=yes-force",
				"SUBAGENT_PARENT_X=extra-sub",
			},
		},
		{
			name: "prefix keys are filtered",
			setEnv: map[string]string{
				"ARGENT_PARENT_X":   parentVal,
				"SUBAGENT_PARENT_X": subParentVal,
			},
			wantGone: []string{parentVal, subParentVal},
		},
		{
			name: "a value containing '=' is preserved and split on first '=' only",
			setEnv: map[string]string{
				"KEEP_EQUALS_VAR":   equalsVal,
				"ARGENT_SESSION_ID": sessionEqVal,
			},
			wantGone: []string{sessionEqVal},
			wantKept: []string{"KEEP_EQUALS_VAR=" + equalsVal},
		},
		{
			name:     "mixed-case private key follows platform case rules",
			setEnv:   map[string]string{mixedCaseKey: caseVal},
			wantGone: mixedCaseWantGone,
			wantKept: mixedCaseWantKept,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.setEnv {
				t.Setenv(k, v)
			}
			got := SanitizeEnv(tc.extra)

			for _, v := range tc.wantGone {
				if envHasValue(got, v) {
					t.Errorf("host-private value %q leaked into child env", v)
				}
			}
			for _, want := range tc.wantKept {
				if !envHasEntry(got, want) {
					t.Errorf("entry %q missing from child env", want)
				}
			}
			// Explicit entries must land at the end, verbatim and in order.
			if len(tc.extra) > 0 {
				if len(got) < len(tc.extra) {
					t.Fatalf("result shorter than extra: len=%d, extra=%d", len(got), len(tc.extra))
				}
				suffix := got[len(got)-len(tc.extra):]
				for i := range tc.extra {
					if suffix[i] != tc.extra[i] {
						t.Errorf("extra[%d] = %q, want %q (not appended verbatim at end)", i, suffix[i], tc.extra[i])
					}
				}
			}
		})
	}
}

// TestIsHostPrivateEnvEntry exercises entry parsing that t.Setenv cannot
// produce directly: bare KEY entries and values containing '='.
func TestIsHostPrivateEnvEntry(t *testing.T) {
	type entryCase struct {
		entry string
		want  bool
	}
	cases := []entryCase{
		{"ARGENT_SESSION_ID=abc", true},
		{"ARGENT_SESSION_ID", true}, // bare key
		{"ARGENT_RUN_ID=abc", true},
		{"ARGENT_PARENT_X=abc", true},
		{"SUBAGENT_PARENT_X=abc", true},
		{"ARGENT_SESSION_ID=has=equals=in=value", true}, // split on first '=' only
		{"PATH=/usr/bin", false},
		{"MY_ARGENT_SESSION_ID=x", false},
		{"ARGENT_SESSION=x", false},
	}
	if runtime.GOOS == "windows" {
		cases = append(cases,
			entryCase{"argent_session_id=abc", true},
			entryCase{"argent_parent_x=abc", true},
		)
	} else {
		cases = append(cases,
			entryCase{"argent_session_id=abc", false},
			entryCase{"argent_parent_x=abc", false},
		)
	}

	for _, tc := range cases {
		if got := isHostPrivateEnvEntry(tc.entry); got != tc.want {
			t.Errorf("isHostPrivateEnvEntry(%q) = %v, want %v", tc.entry, got, tc.want)
		}
	}
}
