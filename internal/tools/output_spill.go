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
// Outputs that carry an intact marker naming a real artifact this package wrote
// are returned unchanged, preventing a second spill when a spilled result is
// post-processed again. A mere substring of the marker in ordinary tool output
// is NOT trusted: doing so would let arbitrary output bypass the bound entirely.
// Cuts always land on UTF-8 rune boundaries, so CJK text and emoji are intact.
func SpillLargeOutput(output string, threshold int, dir string) (result string, artifactPath string, err error) {
	// A non-positive threshold disables spilling; at or below the threshold the
	// output already fits the context budget and is returned verbatim.
	if threshold <= 0 || len(output) <= threshold {
		return output, "", nil
	}
	// Already spilled: never spill a second time. Trust only an intact marker
	// that names a real artifact, not a substring that happens to appear in
	// untrusted tool output (which would bypass the bound entirely).
	if isOwnSpillResult(output, dir) {
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

// spillResultArtifacts scans text for complete markers emitted by
// formatSpillMarker and returns the recovery paths they advertise. Matching the
// whole marker (a numeric elision count, the link and the closing tag) instead
// of just its "\n[output-spill: " prefix is essential: the prefix alone can
// occur in arbitrary tool output, and treating that as "already spilled" would
// skip both spilling and the ordinary output cap.
func spillResultArtifacts(text string) []string {
	const elided = " bytes elided"
	const link = "; full output: "
	var paths []string
	for from := 0; ; {
		i := strings.Index(text[from:], spillMarkerOpen)
		if i < 0 {
			break
		}
		start := from + i
		from = start + len(spillMarkerOpen)
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
		k := strings.Index(rest, spillMarkerEnd)
		if k <= 0 {
			continue
		}
		paths = append(paths, rest[:k])
	}
	return paths
}

// isOwnSpillResult reports whether text is a result this package actually
// spilled: some marker must name an existing, canonically-named artifact under
// the configured directory. A forged marker in untrusted tool output cannot
// satisfy that, so such output is bounded normally instead of being passed
// through verbatim.
func isOwnSpillResult(text, dir string) bool {
	paths := spillResultArtifacts(text)
	if len(paths) == 0 {
		return false
	}
	if dir == "" {
		dir = "."
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	for _, p := range paths {
		absPath, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(absDir, absPath)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if !spillArtifactName(filepath.Base(absPath)) {
			continue
		}
		if info, err := os.Stat(absPath); err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	return false
}

// spillArtifactName is the filename contract of writeSpillArtifact: the fixed
// prefix, a 16-character lowercase hex id and the fixed suffix. It is kept in
// production code (rather than reusing the test-only regexp) so idempotence
// checks cannot be fooled by a look-alike path.
func spillArtifactName(name string) bool {
	if !strings.HasPrefix(name, spillArtifactPrefix) || !strings.HasSuffix(name, spillArtifactSuffix) {
		return false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(name, spillArtifactPrefix), spillArtifactSuffix)
	if len(id) != 16 {
		return false
	}
	for _, c := range id {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
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
