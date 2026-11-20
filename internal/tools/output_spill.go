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

	spillArtifactPrefix = "tool-output-"
	spillArtifactSuffix = ".log"
)

// SpillLargeOutput bounds a tool output for the model context. When output is
// longer than threshold bytes it writes the full text to a fresh artifact under
// dir and returns a head+tail view (middle elided) whose marker names the
// artifact path, so the dropped bytes stay recoverable.
//
// 失败安全：写工件是尽力而为的。写入失败时输出仍会被截断，只是没有恢复链接，
// 且 err 保持为 nil —— spill 失败绝不能把一次成功的工具调用变成错误。返回的 err
// 是保留字段且恒为 nil；调用方不得用它判断原始工具是否成功。elision 标记出现但
// artifactPath 为空，意味着写入失败，或恢复路径太长而主动省略了链接。
//
// 带有完整标记、且标记指向本包真实写出的工件的输出会被原样返回，避免二次 spill。
// 普通工具输出里仅出现标记片段不会被信任：否则任意输出都能绕过字节上限。
// 切分点始终落在 UTF-8 字形边界上，因此中文和 emoji 不会被切坏。
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

	// 先落盘再切分窗口：恢复链接里含有工件的完整路径，固定预留量覆盖不了长目录，
	// 结果会重新超过阈值。必须先拿到路径，才能按标记实际占用的字节数预留预算。
	path, writeErr := writeSpillArtifact(dir, []byte(output))
	if writeErr != nil {
		path = ""
	}

	// 用总长度充当被省略字节数的上界来估标记大小，保证预留量不小于最终标记长度；
	// 真实省略数只会更短，重建后的结果因此不会超过阈值。
	reserve := len(formatSpillMarker(len(output), path))
	if reserve >= threshold && path != "" {
		// 路径本身已占满预算：退化为不带恢复链接的标记，工件仍已落盘，
		// 只是不再通告路径，以免标记把结果撑过阈值。
		path = ""
		reserve = len(formatSpillMarker(len(output), ""))
		if reserve > threshold {
			reserve = threshold
		}
	}
	head, tail := spillWindows(output, threshold, reserve)
	omitted := len(output) - len(head) - len(tail)

	return head + formatSpillMarker(omitted, path) + tail, path, nil
}

// spillWindows 返回超长输出保留的头部与尾部切片，两端都落在 UTF-8 边界上。
// reserve 是标记（含恢复路径）占用的字节数，窗口预算必须把它扣掉，
// 否则重建后的结果会重新超过阈值。
func spillWindows(output string, threshold, reserve int) (head, tail string) {
	budget := threshold - reserve
	if budget < 0 {
		budget = 0
	}
	headBytes := budget / 2
	tailBytes := budget - headBytes

	// trimPartialRune（来自 tools_clip.go）把字节偏移回退到最近的字形起点，
	// 正好是头部窗口需要的下取整边界。
	headEnd := trimPartialRune(output, headBytes)

	// 尾部锚定在字符串末尾，因此起点要向前移动到下一个字形起点；
	// 回退会检查错误的字节，可能让后缀从半个字形开始。
	tailStart := len(output) - tailBytes
	tailStart = spillRuneCeil(output, tailStart)

	// 防御性检查：head 与 tail 不得重叠。
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

// spillArtifactAbsInDir reports whether p is a canonically-named, regular
// artifact located inside dir, returning its absolute path when valid. This is
// the single source of truth for artifact path validation: the idempotence
// guard (isOwnSpillResult) and the line-range reader (readSpillRangeInDir)
// share it, so a forged or escaping path cannot be mistaken for one of our
// artifacts. The checks are unchanged from the original inlined version.
func spillArtifactAbsInDir(dir, p string) (string, bool) {
	if strings.TrimSpace(p) == "" {
		return "", false
	}
	if dir == "" {
		dir = "."
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	absPath, err := filepath.Abs(p)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(absDir, absPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	if !spillArtifactName(filepath.Base(absPath)) {
		return "", false
	}
	info, err := os.Stat(absPath)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	return absPath, true
}

// isOwnSpillResult reports whether text is a result this package actually
// spilled: some marker must name an existing, canonically-named artifact under
// the configured directory. A forged marker in untrusted tool output cannot
// satisfy that, so such output is bounded normally instead of being passed
// through verbatim.
func isOwnSpillResult(text, dir string) bool {
	for _, p := range spillResultArtifacts(text) {
		if _, ok := spillArtifactAbsInDir(dir, p); ok {
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
