package app

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/tools"
)

// Output-artifact spilling is wired here, at the one place allowed to see both
// the resolved storage directory and the tool registry. The tools package owns
// the mechanism (WithOutputSpill / SpillLargeOutput); this file only decides
// whether a run opts in and where the artifacts live.
const (
	// outputSpillEnableEnv is the single, simple opt-in. Truthy values mirror
	// the repo's other feature gates ("1", "true", "yes", "on"). With it unset,
	// configureOutputSpill returns ctx untouched and every existing run keeps
	// byte-identical tool output.
	outputSpillEnableEnv = "ARGENT_OUTPUT_SPILL"

	// These two names mirror the tools package's spillConfig env switches so an
	// operator can override the directory or the byte threshold at assembly
	// time. They are kept as literals here to avoid widening the tools API.
	outputSpillDirEnv       = "ARGENT_OUTPUT_SPILL_DIR"
	outputSpillThresholdEnv = "ARGENT_OUTPUT_SPILL_THRESHOLD"

	// spillArtifactDirName is the convention: artifacts sit under the resolved
	// storage directory, not under the workspace, so they never pollute a repo
	// and are shared by every session/run of this installation.
	spillArtifactDirName = "artifacts"
)

// outputSpillArtifactDir resolves the stable artifact directory for a run. The
// storage directory is always set by Resolve in real runs; the os.TempDir()
// fallback only protects direct callers that build a bare Resolved (e.g. tests)
// from writing a relative "artifacts" directory into the current workspace.
func outputSpillArtifactDir(storage string) string {
	base := strings.TrimSpace(storage)
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, spillArtifactDirName)
}

// outputSpillEnabled reports whether the opt-in switch is truthy.
func outputSpillEnabled(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// configureOutputSpill opts a run's tool outputs into artifact spilling.
//
// Default (no opt-in) is a no-op: ctx is returned unchanged, so the tools
// package sees no config and falls back to the ordinary clipTo path — output
// bytes are exactly as before and no directory is created or scanned.
//
// When opted in via ARGENT_OUTPUT_SPILL, the returned ctx carries
// tools.WithOutputSpill with a stable artifact directory (<storage>/artifacts)
// and the conservative default threshold. ARGENT_OUTPUT_SPILL_DIR and
// ARGENT_OUTPUT_SPILL_THRESHOLD can override either. A one-shot, best-effort
// retention sweep removes artifacts older than tools.DefaultSpillRetention;
// its failure is deliberately ignored so cleanup can never break a run.
func configureOutputSpill(ctx context.Context, env func(string) string, storage string) context.Context {
	get := func(key string) string {
		if env == nil {
			return ""
		}
		return strings.TrimSpace(env(key))
	}
	if !outputSpillEnabled(get(outputSpillEnableEnv)) {
		return ctx
	}

	dir := get(outputSpillDirEnv)
	if dir == "" {
		dir = outputSpillArtifactDir(storage)
	}
	threshold := tools.DefaultOutputSpillThreshold
	if raw := get(outputSpillThresholdEnv); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			threshold = n
		}
	}

	_, _ = tools.CleanSpillArtifacts(dir, tools.DefaultSpillRetention)
	return tools.WithOutputSpill(ctx, threshold, dir)
}
