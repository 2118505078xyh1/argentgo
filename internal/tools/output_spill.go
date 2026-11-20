package tools

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Centralized artifact spill for large tool results, ported from the omp audit
// (output-meta.ts). It is a standalone utility: it is NOT wired into the tool
// execution loop yet. Wiring notes live at the bottom of this file.

const (
	// spillMarkerOpen tags the middle-elision marker this package inserts. The
	// marker's presence means the output has already been spilled, so a second
	// SpillLargeOutput call is a no-op (no duplicate artifact, no misleading
	// second recovery link). An ASCII tag keeps the guard stable across locales
	// and independent of the marker's changing byte count.
	spillMarkerOpen = "\n[output-spill: "
	spillMarkerEnd  = "]\n"

	// spillMarkerReserve leaves room in the byte budget for the elision marker
	// and the recovery path, so the returned body stays under the threshold.
	spillMarkerReserve = 256

	spillArtifactPrefix = "tool-output-"
	spillArtifactSuffix = ".log"
)

// SpillLargeOutput bounds a tool output for the model context. When output is
// longer than threshold bytes it writes the full text to a fresh artifact under
// dir and returns a head+tail view (middle elided) whose marker names the
// artifact path, so the dropped bytes stay recoverable.
//
// Failure safety: writing the artifact is best-effort. On a write failure the
// output is STILL truncated, but without a recovery path, and err stays nil —
// a spill failure must never turn an otherwise successful tool call into an
// error. The returned err is reserved and always nil; callers must not use it
// to decide whether the originating tool succeeded. The absence of artifactPath
// together with the elision marker is the only failure signal.
//
// Outputs that already carry this package's spill marker are returned unchanged,
// preventing a second spill when a spilled result is post-processed again.
// Cuts always land on UTF-8 rune boundaries, so CJK text and emoji are intact.
func SpillLargeOutput(output string, threshold int, dir string) (result string, artifactPath string, err error) {
	// A non-positive threshold disables spilling; at or below the threshold the
	// output already fits the context budget and is returned verbatim.
	if threshold <= 0 || len(output) <= threshold {
		return output, "", nil
	}
	// Already spilled (successfully or not): never spill a second time.
	if strings.Contains(output, spillMarkerOpen) {
		return output, "", nil
	}

	head, tail := spillWindows(output, threshold)
	omitted := len(output) - len(head) - len(tail)

	// Best-effort persist. A failure only drops the recovery link; the caller
	// still gets the safe, truncated result and a nil error.
	path, writeErr := writeSpillArtifact(dir, []byte(output))
	if writeErr != nil {
		path = ""
	}

	return head + formatSpillMarker(omitted, path) + tail, path, nil
}

// spillWindows returns the head and tail slices kept from an oversized output.
// Both ends are rune-safe. The reserve keeps the marker and recovery path from
// pushing the rebuilt result back over the threshold.
func spillWindows(output string, threshold int) (head, tail string) {
	budget := threshold - spillMarkerReserve
	if budget <= 0 {
		budget = threshold
	}
	headBytes := budget / 2
	tailBytes := budget - headBytes

	// trimPartialRune (from tools_clip.go) backs a byte offset off to the
	// nearest rune start — exactly the floor we want for the head window.
	headEnd := trimPartialRune(output, headBytes)

	// The tail is anchored at the end of the string, so move its start FORWARD
	// to the next rune start; backing off would inspect the wrong byte and could
	// leave the suffix beginning mid-rune.
	tailStart := len(output) - tailBytes
	tailStart = spillRuneCeil(output, tailStart)

	// Defensive: head+tail must not overlap. With budget <= threshold < len this
	// cannot normally happen, but keep the invariant explicit.
	if headEnd > tailStart {
		mid := trimPartialRune(output, len(output)/2)
		headEnd, tailStart = mid, spillRuneCeil(output, mid)
	}

	return output[:headEnd], output[tailStart:]
}

// spillRuneCeil returns the smallest i >= at that starts a UTF-8 rune.
func spillRuneCeil(s string, at int) int {
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

// formatSpillMarker renders the middle-elision marker. The recovery path is
// included only when the artifact was written successfully.
func formatSpillMarker(omitted int, path string) string {
	if path == "" {
		return fmt.Sprintf("%s%d bytes elided%s", spillMarkerOpen, omitted, spillMarkerEnd)
	}
	return fmt.Sprintf("%s%d bytes elided; full output: %s%s", spillMarkerOpen, omitted, path, spillMarkerEnd)
}

// writeSpillArtifact persists the full output under dir with a safe,
// collision-resistant filename. os.OpenFile with O_EXCL guarantees we never
// overwrite an existing file, and the hex id contains no path separators or
// shell metacharacters. It returns the created path or an error; callers decide
// how to surface (or swallow) that error.
func writeSpillArtifact(dir string, data []byte) (string, error) {
	if dir == "" {
		dir = "."
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}

	const attempts = 8
	for i := 0; i < attempts; i++ {
		id, err := randomSpillID()
		if err != nil {
			return "", err
		}
		name := spillArtifactPrefix + id + spillArtifactSuffix
		path := filepath.Join(dir, name)

		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue // astronomically unlikely; try a fresh id
		}
		if err != nil {
			return "", err
		}

		if _, werr := f.Write(data); werr != nil {
			f.Close()
			os.Remove(path) // do not leave a partial artifact behind
			return "", werr
		}
		if cerr := f.Close(); cerr != nil {
			os.Remove(path)
			return "", cerr
		}
		return path, nil
	}
	return "", fmt.Errorf("tools: could not create a unique spill artifact in %s", dir)
}

// randomSpillID returns 16 lowercase hex characters from the crypto RNG.
func randomSpillID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// ---------------------------------------------------------------------------
// Wiring notes (not implemented here, by design — this utility is optional):
//
//   - Where: after a tool's output is produced and before it is appended to the
//     model context / session transcript. In this repo the ingress points are
//     `clip(ctx, text)` and its callers (tools_builtin_bash.go,
//     tools_builtin_read.go, tools_builtin_search.go, git_status.go, glob.go,
//     repo_map.go) plus the TUI's `AppendTool`/`truncateToolOutput` in
//     internal/tui/model_mutate.go. A single adapter that calls
//     SpillLargeOutput(rawOutput, threshold, artifactDir) at those ingress
//     points would give the model both a bounded body and a recovery path.
//   - artifactDir convention: this codebase has no session artifact directory
//     yet. Candidates are a per-session dir under the session store, or a
//     `.argent/artifacts/` dir under the workspace (see the security note on
//     never committing `.argent/`). The dir is intentionally a parameter here.
//   - On a non-nil artifactPath, callers may emit an advisory notice ("full
//     output saved to <path>"); the marker already contains the path, so a
//     separate notice is optional. Never fail the tool on a spill write error.
// ---------------------------------------------------------------------------
