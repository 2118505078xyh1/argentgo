package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

// This file bounds very large MCP tool outputs before they reach the model
// context. The goose audit found that CallTool joined every text/resource block
// verbatim, so a single chatty server could flood the context; built-in tools
// already spill oversized output to an artifact (internal/tools/output_spill*.go)
// and this brings the same cap + recovery link to MCP.
//
// Design constraints (mirrors blob.go):
//   - Default behaviour is byte-for-byte unchanged. Spilling only runs when an
//     artifact directory is explicitly configured via ARGENT_MCP_SPILL_DIR (and
//     CallTool additionally reads ARGENT_MCP_SPILL_THRESHOLD). With neither set,
//     spillMCPOutput is a no-op and CallTool keeps its legacy code path.
//   - Stdlib only; the artifact conventions (0700 dir, 0600 O_EXCL file, random
//     hex name, best-effort writes that never fail the originating call) are
//     reused from SaveBlobArtifact rather than importing internal/tools.
//   - The marker format is the SAME as internal/tools' SpillLargeOutput, so the
//     model and any recovery tooling see one consistent contract:
//     "\n[output-spill: N bytes elided; full output: PATH]\n".
//   - Cuts always land on UTF-8 rune boundaries, so CJK/emoji are never split.

const (
	// mcpSpillDirEnv opts CallTool output spilling into writing artifacts. Empty
	// or unset keeps the legacy behaviour (no spilling at all).
	mcpSpillDirEnv = "ARGENT_MCP_SPILL_DIR"
	// mcpSpillThresholdEnv optionally overrides the byte threshold used by
	// CallTool. A non-positive or unparsable value selects the default.
	mcpSpillThresholdEnv = "ARGENT_MCP_SPILL_THRESHOLD"
	// defaultMCPSpillThreshold matches tools.DefaultOutputSpillThreshold: well
	// above the ordinary clip bound, so only genuinely huge outputs spill.
	defaultMCPSpillThreshold = 200 << 10 // 200 KiB

	// Marker strings are copied verbatim from internal/tools/output_spill.go so
	// the two packages emit the identical, recognised recovery marker.
	mcpSpillMarkerOpen    = "\n[output-spill: "
	mcpSpillMarkerEnd     = "]\n"
	mcpSpillMarkerReserve = 256 // room for the marker + recovery path in the budget
)

// mcpSpillDir returns the configured artifact directory. Empty means spilling
// is disabled and both spillMCPOutput and CallTool keep their legacy behaviour.
func mcpSpillDir() string {
	return strings.TrimSpace(os.Getenv(mcpSpillDirEnv))
}

// mcpSpillThreshold returns the byte threshold CallTool uses for spilling. It
// is read from the env so the integration can be tuned without a rebuild; the
// default keeps ordinary MCP replies untouched.
func mcpSpillThreshold() int {
	n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(mcpSpillThresholdEnv)))
	if err != nil || n <= 0 {
		return defaultMCPSpillThreshold
	}
	return n
}

// spillMCPOutput bounds one MCP tool result for the model context. When text is
// longer than threshold bytes AND an artifact directory is configured, it writes
// the full text to a fresh artifact under that directory and returns a
// head+tail view (middle elided) carrying the recovery path marker. Otherwise it
// returns the text verbatim with spilled == false.
//
// It never fails the originating tool call: on a write failure the text is still
// bounded, but the marker omits the recovery path and artifactPath is empty.
// Cuts land on UTF-8 rune boundaries, so CJK text and emoji stay intact.
func spillMCPOutput(text string, threshold int) (result string, artifactPath string, spilled bool) {
	// Non-positive threshold and at-or-below-threshold output need no work.
	if threshold <= 0 || len(text) <= threshold {
		return text, "", false
	}
	dir := mcpSpillDir()
	if dir == "" {
		// No opt-in directory: legacy behaviour, never spill.
		return text, "", false
	}
	// An intact marker naming one of our real artifacts means this text was
	// already spilled; do not spill (or truncate) it a second time.
	if isMCPSpillResult(text, dir) {
		return text, "", false
	}

	head, tail := mcpSpillWindows(text, threshold)
	omitted := len(text) - len(head) - len(tail)

	// Best-effort persistence: a failure only drops the recovery link.
	path, err := SaveBlobArtifact(dir, []byte(text), ".txt")
	if err != nil {
		path = ""
	}
	return head + formatMCPSpillMarker(omitted, path) + tail, path, true
}

// mcpSpillWindows returns the rune-safe head and tail slices kept from an
// oversized result. The reserve keeps the marker and recovery path from pushing
// the rebuilt result back over the threshold.
func mcpSpillWindows(text string, threshold int) (head, tail string) {
	budget := threshold - mcpSpillMarkerReserve
	if budget <= 0 {
		budget = threshold
	}
	headBytes := budget / 2
	tailBytes := budget - headBytes

	headEnd := mcpRuneFloor(text, headBytes)
	tailStart := mcpRuneCeil(text, len(text)-tailBytes)
	if headEnd > tailStart {
		mid := mcpRuneFloor(text, len(text)/2)
		headEnd, tailStart = mid, mcpRuneCeil(text, mid)
	}
	return text[:headEnd], text[tailStart:]
}

// mcpRuneFloor returns the largest i <= at that starts a UTF-8 rune.
func mcpRuneFloor(s string, at int) int {
	if at <= 0 {
		return 0
	}
	if at >= len(s) {
		return len(s)
	}
	i := at
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return i
}

// mcpRuneCeil returns the smallest i >= at that starts a UTF-8 rune.
func mcpRuneCeil(s string, at int) int {
	if at <= 0 {
		return 0
	}
	if at >= len(s) {
		return len(s)
	}
	i := at
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return i
}

// formatMCPSpillMarker renders the middle-elision marker. The recovery path is
// included only when the artifact was written successfully.
func formatMCPSpillMarker(omitted int, path string) string {
	if path == "" {
		return fmt.Sprintf("%s%d bytes elided%s", mcpSpillMarkerOpen, omitted, mcpSpillMarkerEnd)
	}
	return fmt.Sprintf("%s%d bytes elided; full output: %s%s", mcpSpillMarkerOpen, omitted, path, mcpSpillMarkerEnd)
}

// isMCPSpillResult reports whether text is a result this feature actually
// spilled: some complete marker must name an existing, canonically-named
// artifact under dir. A forged marker substring in untrusted server output is
// NOT trusted, so such output is bounded normally instead of passing through.
func isMCPSpillResult(text, dir string) bool {
	const (
		elided = " bytes elided"
		link   = "; full output: "
	)
	for from := 0; ; {
		i := strings.Index(text[from:], mcpSpillMarkerOpen)
		if i < 0 {
			return false
		}
		start := from + i
		from = start + len(mcpSpillMarkerOpen)
		rest := text[from:]

		j := strings.Index(rest, elided)
		if j <= 0 {
			continue
		}
		digits := rest[:j]
		numeric := true
		for _, c := range digits {
			if c < '0' || c > '9' {
				numeric = false
				break
			}
		}
		if !numeric {
			continue
		}
		rest = rest[j+len(elided):]
		if !strings.HasPrefix(rest, link) {
			continue
		}
		rest = rest[len(link):]
		k := strings.Index(rest, mcpSpillMarkerEnd)
		if k <= 0 {
			continue
		}
		if _, ok := mcpArtifactAbsInDir(dir, rest[:k]); ok {
			return true
		}
	}
}

// mcpArtifactAbsInDir validates p as a canonical artifact produced by
// SaveBlobArtifact inside dir, returning its absolute path when valid.
func mcpArtifactAbsInDir(dir, p string) (string, bool) {
	if strings.TrimSpace(p) == "" || strings.TrimSpace(dir) == "" {
		return "", false
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	absPath, err := filepath.Abs(p)
	if err != nil || !pathWithinDir(absDir, absPath) {
		return "", false
	}
	if !mcpArtifactName(filepath.Base(absPath)) {
		return "", false
	}
	info, err := os.Stat(absPath)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	return absPath, true
}

// mcpArtifactName checks the filename contract of SaveBlobArtifact: the fixed
// blob prefix, a 16-character lowercase hex id and an optional safe extension.
func mcpArtifactName(name string) bool {
	if !strings.HasPrefix(name, blobArtifactPrefix) {
		return false
	}
	rest := strings.TrimPrefix(name, blobArtifactPrefix)
	id := rest
	if dot := strings.IndexByte(rest, '.'); dot >= 0 {
		id = rest[:dot]
		ext := rest[dot+1:]
		if ext == "" || len(ext) > maxArtifactExtLen {
			return false
		}
		for _, c := range ext {
			if !('0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z') {
				return false
			}
		}
	}
	if len(id) != blobIDBytes*2 {
		return false
	}
	for _, c := range id {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}
