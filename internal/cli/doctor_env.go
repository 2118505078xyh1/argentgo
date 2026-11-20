package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// ProbeResult is one structured environment observation produced by
// ProbeEnvironment. Value holds the raw observed value (never sliced in the
// middle of a UTF-8 rune, so CJK values survive intact), OK reports whether the
// observation is healthy, and Detail explains the result in human-readable
// text. Fields are exported so callers and tests can inspect them directly.
type ProbeResult struct {
	Name   string
	Value  string
	OK     bool
	Detail string
}

// Probe names. Kept as constants so doctorAdvice, the formatter and tests
// share a single vocabulary instead of repeating string literals.
const (
	probeGoVersion = "go_version"
	probePlatform  = "platform"
	probeShell     = "shell"
	probeTerminal  = "terminal"
	probeGit       = "git"
	probeEnv       = "env"
)

// envGetter is the injection seam used by probeEnvironment so tests can supply
// a controlled environment without mutating the process environment.
type envGetter func(string) string

// lookPathFunc mirrors exec.LookPath so git detection can be faked in tests.
type lookPathFunc func(string) (string, error)

// doctorKeyEnvVars are the environment variables whose presence doctor reports
// in the "env" probe. The order is fixed so the probe's Value is deterministic.
var doctorKeyEnvVars = []string{"PATH", "HOME", "USERPROFILE", "TMPDIR", "TEMP", "TMP"}

// doctorEnvGroups defines the "at least one of" groups that must be present for
// the "env" probe to be healthy. A platform normally satisfies each group via a
// different variable (HOME on POSIX, USERPROFILE on Windows), hence groups
// instead of a flat required list.
var doctorEnvGroups = []struct {
	vars []string
	desc string
}{
	{vars: []string{"PATH"}, desc: "PATH"},
	{vars: []string{"HOME", "USERPROFILE"}, desc: "HOME/USERPROFILE"},
	{vars: []string{"TMPDIR", "TEMP", "TMP"}, desc: "TMPDIR/TEMP/TMP"},
}

// ProbeEnvironment probes the local runtime and environment and returns a
// stable, ordered slice of observations. It writes nothing to doctor's output
// streams and does not mutate process state; it only reads runtime values, the
// process environment and the git lookup.
func ProbeEnvironment() []ProbeResult {
	return probeEnvironment(os.Getenv, exec.LookPath)
}

// probeEnvironment is the pure core behind ProbeEnvironment. Every external
// input is injected, which makes the result fully deterministic under tests.
func probeEnvironment(env envGetter, lookPath lookPathFunc) []ProbeResult {
	probes := make([]ProbeResult, 0, 6)

	// Go version and target platform are properties of the running binary, so
	// they are always healthy; they are reported purely for context.
	probes = append(probes, ProbeResult{
		Name:   probeGoVersion,
		Value:  runtime.Version(),
		OK:     true,
		Detail: "Go runtime version",
	})
	probes = append(probes, ProbeResult{
		Name:   probePlatform,
		Value:  runtime.GOOS + "/" + runtime.GOARCH,
		OK:     true,
		Detail: "GOOS/GOARCH",
	})

	// Shell: SHELL on POSIX shells, COMSPEC on Windows. We store the name (last
	// path component) so the value stays short and stable across absolute paths.
	shellRaw := firstNonEmpty(env("SHELL"), env("COMSPEC"))
	shellOK := shellRaw != ""
	shellDetail := "shell detected"
	if !shellOK {
		shellDetail = "SHELL/COMSPEC not set"
	}
	probes = append(probes, ProbeResult{
		Name:   probeShell,
		Value:  shellName(shellRaw),
		OK:     shellOK,
		Detail: shellDetail,
	})

	// Terminal: TERM on POSIX terminals, WT_SESSION inside Windows Terminal.
	term := env("TERM")
	wtSession := env("WT_SESSION")
	terminalValue := firstNonEmpty(term, wtSession)
	terminalOK := terminalValue != ""
	terminalDetail := "TERM/WT_SESSION detected"
	if !terminalOK {
		terminalDetail = "TERM/WT_SESSION not set (non-interactive?)"
	}
	probes = append(probes, ProbeResult{
		Name:   probeTerminal,
		Value:  terminalValue,
		OK:     terminalOK,
		Detail: terminalDetail,
	})

	// Git availability. Mirrors checkGit: missing git is a warning rather than
	// a failure, so OK=false here never changes doctor's exit code.
	gitPath, err := lookPath("git")
	gitOK := err == nil
	gitDetail := "git on PATH"
	if !gitOK {
		gitDetail = "git not found on PATH"
	}
	probes = append(probes, ProbeResult{
		Name:   probeGit,
		Value:  gitPath,
		OK:     gitOK,
		Detail: gitDetail,
	})

	// Key environment variable presence. "present" is reported as a stable,
	// comma-joined list; "healthy" requires every group to have at least one
	// member set.
	present := make([]string, 0, len(doctorKeyEnvVars))
	for _, key := range doctorKeyEnvVars {
		if isSet(env(key)) {
			present = append(present, key)
		}
	}
	missing := make([]string, 0, len(doctorEnvGroups))
	for _, group := range doctorEnvGroups {
		satisfied := false
		for _, key := range group.vars {
			if isSet(env(key)) {
				satisfied = true
				break
			}
		}
		if !satisfied {
			missing = append(missing, group.desc)
		}
	}
	envDetail := "all key variables present"
	if len(missing) > 0 {
		envDetail = "missing groups: " + strings.Join(missing, ", ")
	}
	probes = append(probes, ProbeResult{
		Name:   probeEnv,
		Value:  strings.Join(present, ","),
		OK:     len(missing) == 0,
		Detail: envDetail,
	})

	return probes
}

// shellName returns the last path component of a shell value. It splits on both
// POSIX and Windows separators explicitly (rather than filepath.Base) so the
// result is identical on every host, keeping probe output stable in
// cross-platform tests. Slicing only at ASCII separators is rune-safe.
func shellName(value string) string {
	if i := strings.LastIndexAny(value, `/\`); i >= 0 {
		return value[i+1:]
	}
	return value
}

func isSet(value string) bool {
	return strings.TrimSpace(value) != ""
}

// doctorAdvice turns unhealthy probe results into human-readable fix
// suggestions. Output follows probe order, so it is deterministic; healthy
// probes produce no advice.
func doctorAdvice(probes []ProbeResult) []string {
	var advice []string
	for _, p := range probes {
		if p.OK {
			continue
		}
		switch p.Name {
		case probeShell:
			advice = append(advice, "shell: set SHELL (POSIX) or COMSPEC (Windows) so the shell can be identified")
		case probeTerminal:
			advice = append(advice, "terminal: TERM/WT_SESSION is unset; run inside a real terminal for full TUI support")
		case probeGit:
			advice = append(advice, "git: install Git and add it to PATH (https://git-scm.com)")
		case probeEnv:
			advice = append(advice, "env: set the missing key variables ("+p.Detail+")")
		default:
			advice = append(advice, fmt.Sprintf("%s: review this environment probe", p.Name))
		}
	}
	return advice
}

// writeDoctorEnvironment appends the structured environment section to doctor's
// output. It is intentionally written last so every pre-existing doctor line
// remains a byte-for-byte prefix of the new output. fmt's %-Ns padding counts
// runes, so CJK values and details are never truncated mid-rune.
func writeDoctorEnvironment(w io.Writer, probes []ProbeResult, advice []string) {
	fmt.Fprintln(w, "\nenvironment:")
	for _, p := range probes {
		status := statusWarn
		if p.OK {
			status = statusOK
		}
		fmt.Fprintf(w, "  %-4s %-10s %s %s\n", status, p.Name, p.Value, p.Detail)
	}
	if len(advice) > 0 {
		fmt.Fprintln(w, "  advice:")
		for _, line := range advice {
			fmt.Fprintf(w, "    - %s\n", line)
		}
	}
}
