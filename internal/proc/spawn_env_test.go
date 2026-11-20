package proc

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

// envProbeMarker turns the re-executed test binary into a tiny env dumper.
const envProbeMarker = "ARGENT_PROC_ENV_PROBE"

// envProbeKeys is the fixed set of variables the helper reports. Host-private
// keys use no suffixes so the exact-name and prefix denylists are both covered.
var envProbeKeys = []string{
	"ARGENT_SESSION_ID",
	"ARGENT_RUN_ID",
	"ARGENT_PARENT_X",
	"SUBAGENT_PARENT_X",
	"PROC_ENV_NORMAL",
	"PROC_ENV_EXTRA",
}

// TestProcEnvProbeHelper is a no-op during the normal run. Spawn tests re-exec
// the test binary with the marker set; the helper prints the values it sees so
// the parent can assert exactly which variables a child inherited.
func TestProcEnvProbeHelper(t *testing.T) {
	if os.Getenv(envProbeMarker) == "" {
		return
	}
	for _, key := range envProbeKeys {
		_, _ = fmt.Fprintf(os.Stdout, "PROBE:%s=%s\n", key, os.Getenv(key))
	}
}

func envProbeCommand(extra []string) Command {
	env := append([]string{envProbeMarker + "=1"}, extra...)
	return Command{
		Path: os.Args[0],
		Args: []string{"-test.run=^TestProcEnvProbeHelper$"},
		Env:  env,
	}
}

// parseEnvProbe keeps only the helper's own lines; the test framework also
// writes "PASS"/"ok" lines to stdout.
func parseEnvProbe(out string) map[string]string {
	got := make(map[string]string)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "PROBE:") {
			continue
		}
		key, value, _ := strings.Cut(strings.TrimPrefix(line, "PROBE:"), "=")
		got[key] = value
	}
	return got
}

// TestSpawnPathsFilterHostPrivateEnv checks that every spawn path in the
// package gives children the same sanitized environment. Run has always
// filtered host-private variables; the detached path (used by RunWithTimeout
// for promoted background jobs, i.e. nested agents) must do the same, or a
// nested agent mistakes the host's session plumbing for its own.
func TestSpawnPathsFilterHostPrivateEnv(t *testing.T) {
	const (
		sessionVal   = "sentinel-session-9f3a"
		runVal       = "sentinel-run-7c1b"
		parentVal    = "sentinel-parent-2d8e"
		subParentVal = "sentinel-subparent-4a6f"
		normalVal    = "sentinel-normal-5b2c"
		extraVal     = "sentinel-extra-1c7e"
		extraPrivVal = "sentinel-extra-private-3e9d"
	)

	spawns := []struct {
		name string
		run  func(Command) (string, error)
	}{
		{
			name: "Run",
			run: func(c Command) (string, error) {
				res, err := Run(context.Background(), c)
				return res.Stdout, err
			},
		},
		{
			name: "StartDetached",
			run: func(c Command) (string, error) {
				p, err := StartDetached(c)
				if err != nil {
					return "", err
				}
				_ = p.Wait()
				return p.Output(), nil
			},
		},
	}

	tests := []struct {
		name     string
		setEnv   map[string]string
		extra    []string
		wantGone map[string]string // key -> value that must NOT reach the child
		wantKept map[string]string // key -> value that MUST reach the child
	}{
		{
			name: "exact and prefix host-private keys are filtered",
			setEnv: map[string]string{
				"ARGENT_SESSION_ID": sessionVal,
				"ARGENT_RUN_ID":     runVal,
				"ARGENT_PARENT_X":   parentVal,
				"SUBAGENT_PARENT_X": subParentVal,
				"PROC_ENV_NORMAL":   normalVal,
			},
			wantGone: map[string]string{
				"ARGENT_SESSION_ID": sessionVal,
				"ARGENT_RUN_ID":     runVal,
				"ARGENT_PARENT_X":   parentVal,
				"SUBAGENT_PARENT_X": subParentVal,
			},
			wantKept: map[string]string{"PROC_ENV_NORMAL": normalVal},
		},
		{
			name:   "explicit extra entries are never filtered",
			setEnv: map[string]string{"PROC_ENV_NORMAL": normalVal},
			extra: []string{
				"PROC_ENV_EXTRA=" + extraVal,
				"ARGENT_SESSION_ID=" + extraPrivVal,
			},
			wantGone: map[string]string{"ARGENT_SESSION_ID": sessionVal},
			wantKept: map[string]string{
				"PROC_ENV_NORMAL":   normalVal,
				"PROC_ENV_EXTRA":    extraVal,
				"ARGENT_SESSION_ID": extraPrivVal,
			},
		},
	}

	for _, sp := range spawns {
		for _, tc := range tests {
			t.Run(sp.name+"/"+tc.name, func(t *testing.T) {
				for k, v := range tc.setEnv {
					t.Setenv(k, v)
				}
				out, err := sp.run(envProbeCommand(tc.extra))
				if err != nil {
					t.Fatalf("spawn: %v", err)
				}
				got := parseEnvProbe(out)
				for key, val := range tc.wantGone {
					if got[key] == val {
						t.Errorf("host-private %s=%q leaked into child", key, val)
					}
				}
				for key, val := range tc.wantKept {
					if got[key] != val {
						t.Errorf("child %s = %q, want %q", key, got[key], val)
					}
				}
			})
		}
	}
}
