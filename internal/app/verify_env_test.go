package app

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// verifyEnvHasValue reports whether any "K=V" entry in env has exactly value
// as its value. Tests use unique sentinels set via t.Setenv so the assertions
// are independent of whatever else the parent environment contains.
func verifyEnvHasValue(env []string, value string) bool {
	for _, entry := range env {
		_, v, _ := strings.Cut(entry, "=")
		if v == value {
			return true
		}
	}
	return false
}

// verifyEnvHasEntry reports whether the exact "K=V" entry is present.
func verifyEnvHasEntry(env []string, want string) bool {
	for _, entry := range env {
		if entry == want {
			return true
		}
	}
	return false
}

// TestVerifyEnvironmentSanitizesHostPrivateVars pins the P1 fix: the verify
// child must not inherit the host's session plumbing. proc.SanitizeEnv removes
// the exact names ARGENT_SESSION_ID / ARGENT_RUN_ID and the prefixes
// ARGENT_PARENT_* / SUBAGENT_PARENT_*; the legacy GOFLAGS strip must remain.
func TestVerifyEnvironmentSanitizesHostPrivateVars(t *testing.T) {
	const (
		sessionVal   = "vSentinel-session-9f3a"
		runVal       = "vSentinel-run-7c1b"
		parentVal    = "vSentinel-parent-2d8e"
		subParentVal = "vSentinel-subparent-4a6f"
		goflagsVal   = "-toolexec=evil"
		keepVal      = "vSentinel-keep-5b2c"
	)

	// Mirror the platform case rules used by proc.SanitizeEnv: a mixed-case
	// private key is filtered on Windows and preserved elsewhere.
	mixedCaseKey := "argent_session_id"
	mixedCaseVal := "vSentinel-mixedcase-8e1d"

	t.Setenv("ARGENT_SESSION_ID", sessionVal)
	t.Setenv("ARGENT_RUN_ID", runVal)
	t.Setenv("ARGENT_PARENT_PROBE", parentVal)
	t.Setenv("SUBAGENT_PARENT_PROBE", subParentVal)
	t.Setenv("GOFLAGS", goflagsVal)
	t.Setenv("ARGENT_VERIFY_KEEP", keepVal)
	t.Setenv(mixedCaseKey, mixedCaseVal)

	got := verifyEnvironment()

	// Host-private values must be gone; GOFLAGS must still be stripped.
	for _, leaked := range []string{sessionVal, runVal, parentVal, subParentVal, goflagsVal} {
		if verifyEnvHasValue(got, leaked) {
			t.Errorf("verifyEnvironment leaked private/unsafe value %q", leaked)
		}
	}
	// The mixed-case private key follows the same platform rules as proc:
	// filtered on Windows (case-insensitive), preserved elsewhere.
	if runtime.GOOS == "windows" {
		if verifyEnvHasValue(got, mixedCaseVal) {
			t.Errorf("verifyEnvironment leaked mixed-case private value %q", mixedCaseVal)
		}
	} else if !verifyEnvHasValue(got, mixedCaseVal) {
		t.Errorf("verifyEnvironment dropped the case-sensitive normal variable %q", mixedCaseKey)
	}
	// No GOFLAGS entry under any spelling.
	for _, entry := range got {
		key, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(key, "GOFLAGS") {
			t.Errorf("verifyEnvironment leaked GOFLAGS entry %q", entry)
		}
	}

	// Legitimate variables — a custom operator var plus inherited system vars —
	// pass through unchanged, so verify behaviour does not regress.
	if !verifyEnvHasEntry(got, "ARGENT_VERIFY_KEEP="+keepVal) {
		t.Errorf("verifyEnvironment dropped the normal variable ARGENT_VERIFY_KEEP")
	}
	if _, ok := os.LookupEnv("PATH"); ok && !verifyHasKey(got, "PATH") {
		t.Errorf("verifyEnvironment dropped PATH")
	}
	if len(got) == 0 {
		t.Errorf("verifyEnvironment returned an empty environment")
	}
}

// verifyHasKey reports whether any entry in env has the given key (case-folded,
// matching how on Windows PATH/Path casing can vary).
func verifyHasKey(env []string, key string) bool {
	for _, entry := range env {
		k, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(k, key) {
			return true
		}
	}
	return false
}

// verifyEnvProbeProgram prints a marker for each watched variable it can see.
// It is built into a temp .exe and launched through NewVerifyFunc so the test
// observes the *actual* environment the verify child receives, not just the
// slice returned by verifyEnvironment().
const verifyEnvProbeProgram = `package main

import (
	"fmt"
	"os"
)

func main() {
	for _, k := range []string{
		"ARGENT_SESSION_ID",
		"ARGENT_RUN_ID",
		"ARGENT_PARENT_PROBE",
		"SUBAGENT_PARENT_PROBE",
		"GOFLAGS",
		"ARGENT_VERIFY_KEEP",
	} {
		if v, ok := os.LookupEnv(k); ok {
			fmt.Printf("SEEN %s=%s\n", k, v)
		}
	}
}
`

// buildVerifyEnvProbe builds the env probe inside workspace and returns its
// relative .exe name. It skips when the Go toolchain is unavailable.
func buildVerifyEnvProbe(t *testing.T, workspace string) string {
	t.Helper()
	writeVerifyFile(t, workspace, "go.mod", "module verifyenvprobe\n\ngo 1.21\n")
	writeVerifyFile(t, workspace, "main.go", verifyEnvProbeProgram)

	const exe = "verifyenvprobe.exe"
	cmd := exec.Command("go", "build", "-o", exe, ".")
	cmd.Dir = workspace
	cmd.Env = verifyEnvironment() // build with the same sanitized env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("Go toolchain unavailable for env probe build: %v: %s", err, out)
	}
	return exe
}

// TestVerifyChildCannotSeeHostPrivateEnv is the end-to-end counterpart of the
// filter test: the sentinel values are planted in the parent process, then a
// real verify subprocess reports which variables it can actually see. The
// normal sentinel must be visible (proving env inheritance works), while every
// host-private variable and GOFLAGS must be absent from the child.
func TestVerifyChildCannotSeeHostPrivateEnv(t *testing.T) {
	const (
		sessionVal   = "childSentinel-session-3a7f"
		runVal       = "childSentinel-run-1b9d"
		parentVal    = "childSentinel-parent-6c2e"
		subParentVal = "childSentinel-subparent-0e4a"
		goflagsVal   = "-toolexec=child-evil"
		keepVal      = "childSentinel-keep-8d5b"
	)

	t.Setenv("ARGENT_SESSION_ID", sessionVal)
	t.Setenv("ARGENT_RUN_ID", runVal)
	t.Setenv("ARGENT_PARENT_PROBE", parentVal)
	t.Setenv("SUBAGENT_PARENT_PROBE", subParentVal)
	t.Setenv("GOFLAGS", goflagsVal)
	t.Setenv("ARGENT_VERIFY_KEEP", keepVal)

	workspace := t.TempDir()
	exe := buildVerifyEnvProbe(t, workspace)

	verify := NewVerifyFunc([]string{exe}, 5000, workspace)
	label, passed, output, ok := verify(context.Background(), nil)
	if !ok || !passed {
		t.Fatalf("probe failed to run: (label=%q, passed=%v, output=%q, ok=%v)", label, passed, output, ok)
	}

	// The normal variable proves the child really inherited a filtered env.
	if !strings.Contains(output, "ARGENT_VERIFY_KEEP="+keepVal) {
		t.Errorf("child did not see the normal variable; env inheritance broken: %q", output)
	}
	// No host-private sentinel and no GOFLAGS value may reach the child.
	for _, leaked := range []string{sessionVal, runVal, parentVal, subParentVal, goflagsVal} {
		if strings.Contains(output, leaked) {
			t.Errorf("verify child observed private/unsafe value %q: %q", leaked, output)
		}
	}
	if strings.Contains(output, "SEEN GOFLAGS=") || strings.Contains(output, "SEEN ARGENT_SESSION_ID=") ||
		strings.Contains(output, "SEEN ARGENT_RUN_ID=") || strings.Contains(output, "SEEN ARGENT_PARENT_PROBE=") ||
		strings.Contains(output, "SEEN SUBAGENT_PARENT_PROBE=") {
		t.Errorf("verify child observed a sanitized variable name: %q", output)
	}
}
