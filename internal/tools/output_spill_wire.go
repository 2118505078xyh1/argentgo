package tools

import (
	"context"
	"os"
	"strconv"
	"strings"
)

// This file wires SpillLargeOutput into the tool-output pipeline without
// changing any existing output format. Spilling is strictly opt-in: the ordinary
// clip()/clipTo() path is untouched unless a caller enables it, so the default
// behaviour (and every existing test) is byte-for-byte identical.

const (
	// DefaultOutputSpillThreshold is the conservative "very large" bound used
	// when a caller enables spilling. It is well above maxToolOutput (40 KB), so
	// ordinary large outputs keep the established clipped format.
	DefaultOutputSpillThreshold = 200 << 10 // 200 KiB

	// spillDirEnv and spillThresholdEnv allow enabling spilling without any code
	// change. Both must be set for the env switch to take effect; with neither
	// set, spilling is disabled.
	spillDirEnv       = "ARGENT_OUTPUT_SPILL_DIR"
	spillThresholdEnv = "ARGENT_OUTPUT_SPILL_THRESHOLD"
)

// spillSettings is the opt-in configuration carried in a context.
type spillSettings struct {
	threshold int
	dir       string
}

// enabled reports whether spilling should run. A non-positive threshold or an
// empty directory keeps the feature off, so a half-configured switch can never
// write an artifact to an unintended location.
func (s spillSettings) enabled() bool {
	return s.threshold > 0 && strings.TrimSpace(s.dir) != ""
}

type spillConfigKey struct{}

// WithOutputSpill returns a derived context that opts tool outputs into
// artifact spilling. Outputs larger than threshold are written in full under
// dir and replaced with a bounded head/tail view carrying a recovery link.
// Passing a non-positive threshold or an empty dir explicitly disables
// spilling for that context, overriding the env switch.
func WithOutputSpill(ctx context.Context, threshold int, dir string) context.Context {
	return context.WithValue(ctx, spillConfigKey{}, spillSettings{threshold: threshold, dir: dir})
}

// spillConfig resolves the opt-in settings. An explicit context value wins;
// otherwise the two env vars are checked. Default: disabled.
func spillConfig(ctx context.Context) spillSettings {
	if s, ok := ctx.Value(spillConfigKey{}).(spillSettings); ok {
		return s
	}
	threshold, err := strconv.Atoi(strings.TrimSpace(os.Getenv(spillThresholdEnv)))
	if err != nil {
		threshold = 0
	}
	return spillSettings{threshold: threshold, dir: strings.TrimSpace(os.Getenv(spillDirEnv))}
}

// spillToolOutput is the central normalizer hook. It reports whether it handled
// text; when it returns handled == false the caller must fall back to the
// ordinary clipTo path. Handling covers three cases:
//   - spilling disabled -> not handled (unchanged behaviour);
//   - text carrying an intact spill marker that names one of our artifacts ->
//     returned verbatim, so a spilled result is never spilled or re-clipped a
//     second time; a marker-looking substring in untrusted output is not trusted;
//   - text above the threshold -> spilled and replaced with the bounded view.
//
// Text at or below the threshold is deliberately NOT handled here: the ordinary
// clipTo bound still applies, so raising the spill threshold never silently
// lets a mid-sized output into the model context unclipped.
func spillToolOutput(ctx context.Context, text string) (out string, handled bool) {
	cfg := spillConfig(ctx)
	if !cfg.enabled() {
		return "", false
	}
	if isOwnSpillResult(text, cfg.dir) {
		return text, true
	}
	if len(text) <= cfg.threshold {
		return "", false
	}
	result, _, _ := SpillLargeOutput(text, cfg.threshold, cfg.dir)
	return result, true
}
