// Package eval's edit benchmark measures the *resolution contract* of the
// built-in edit tool under realistic, model-shaped edit scenarios.
//
// Methodology
//
// The edit tool's pure resolution helpers (exact match, the graded repair
// ladder, and hashline anchoring with stale-anchor recovery) live in package
// internal/tools and are deliberately unexported. Importing them would drag in
// the registry, path resolution, mutation locking, the security chain and the
// approval gate; none of those are what we want to measure. This file therefore
// reimplements the resolution contract minimally, mirroring
// internal/tools/tools_builtin_write_edit.go line for line, and applies it to a
// scratch file under a temp directory.
//
// What it measures: given the bytes a model saw (Original) plus the arguments a
// model would send (Old/New/Anchor/AnchorHash/Repair/Hashline), does the
// algorithm produce the expected replacement or the expected error class?
//
// What it does NOT measure: the tool registry, path/workspace validation,
// mutation locking, policy/approval/audit handling, streaming, or any real
// model/network call. It is fully offline and deterministic.
//
// A case "passes" when the observed outcome matches the case's declared
// expectation: WantErrContains asserts an error whose text contains the needle;
// WantResult asserts a successful edit whose final content contains the needle;
// with neither, any successful edit passes. Failure-expected cases are as
// important as success-expected ones: a harness that always reports pass would
// be worthless.

package eval

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Edit modes understood by the benchmark.
const (
	modeExact    = "exact"
	modeRepair   = "repair"
	modeHashline = "hashline"
)

// editBenchFile is the scratch file every case is applied to. It keeps error
// messages stable (the real tool renders the path it was given).
const editBenchFile = "edit.txt"

// EditCase is one scripted edit scenario. Original is the file content the
// model saw; Anchor is the exact text it hashed for hashline mode; AnchorHash is
// the model-supplied hash (empty means "use the canonical sha256-12 of Anchor");
// ShiftLines is text inserted before the match target to simulate lines added
// above it between read and write.
type EditCase struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Original        string `json:"original"`
	Anchor          string `json:"anchor,omitempty"`
	Old             string `json:"old,omitempty"`
	New             string `json:"new"`
	Mode            string `json:"mode"`
	AnchorHash      string `json:"anchor_hash,omitempty"`
	ShiftLines      string `json:"shift_lines,omitempty"`
	WantResult      string `json:"want_result,omitempty"`
	WantErrContains string `json:"want_err_contains,omitempty"`
}

// ModeStat aggregates outcomes for one edit mode.
type ModeStat struct {
	Passed int `json:"passed"`
	Failed int `json:"failed"`
}

// EditCaseResult is the graded outcome of one case. Passed means the algorithm
// matched the case's declared expectation (which may itself be an error).
type EditCaseResult struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Mode   string `json:"mode"`
	Passed bool   `json:"passed"`
	Got    string `json:"got,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// EditBenchReport summarises a whole benchmark run.
type EditBenchReport struct {
	Total    int                 `json:"total"`
	Passed   int                 `json:"passed"`
	Failed   int                 `json:"failed"`
	ByMode   map[string]ModeStat `json:"by_mode"`
	Failures []EditCaseResult    `json:"failures,omitempty"`
}

// LoadEditCases reads a JSON fixture: either a bare array of cases or
// {"cases": [...]}. A UTF-8 BOM is tolerated, matching eval.Load.
func LoadEditCases(path string) ([]EditCase, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cases, err := ParseEditCases(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cases, nil
}

// ParseEditCases decodes and validates a JSON edit-case document.
func ParseEditCases(data []byte) ([]EditCase, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	var document struct {
		Cases []EditCase `json:"cases"`
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		if err := json.Unmarshal(data, &document.Cases); err != nil {
			return nil, fmt.Errorf("decode edit cases: %w", err)
		}
	} else if err := json.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("decode edit cases: %w", err)
	}
	if len(document.Cases) == 0 {
		return nil, fmt.Errorf("no edit cases in document")
	}
	seen := make(map[string]bool, len(document.Cases))
	for i := range document.Cases {
		c := &document.Cases[i]
		if c.ID == "" {
			c.ID = fmt.Sprintf("case-%d", i+1)
		}
		if c.Name == "" {
			c.Name = c.ID
		}
		if c.Mode == "" {
			c.Mode = modeExact
		}
		if c.Mode != modeExact && c.Mode != modeRepair && c.Mode != modeHashline {
			return nil, fmt.Errorf("case %d (%s): invalid mode %q", i+1, c.ID, c.Mode)
		}
		if seen[c.ID] {
			return nil, fmt.Errorf("case %d: duplicate id %q", i+1, c.ID)
		}
		seen[c.ID] = true
		if c.WantResult == "" && c.WantErrContains == "" {
			return nil, fmt.Errorf("case %d (%s): no expectation declared", i+1, c.ID)
		}
	}
	return document.Cases, nil
}

// RunEditCase applies one case to a temp file and grades the result. All I/O is
// confined to a MkdirTemp scratch directory that is removed before returning.
func RunEditCase(c EditCase) EditCaseResult {
	mode := c.Mode
	if mode == "" {
		mode = modeExact
	}
	res := EditCaseResult{ID: c.ID, Name: c.Name, Mode: mode}

	dir, err := os.MkdirTemp("", "editbench-")
	if err != nil {
		res.Reason = "cannot create scratch directory: " + err.Error()
		return res
	}
	defer os.RemoveAll(dir)

	path := filepath.Join(dir, editBenchFile)
	current := buildCurrentContent(c)
	if err := os.WriteFile(path, []byte(current), 0o644); err != nil {
		res.Reason = "cannot seed scratch file: " + err.Error()
		return res
	}
	data, err := os.ReadFile(path)
	if err != nil {
		res.Reason = "cannot read scratch file: " + err.Error()
		return res
	}

	var out string
	var applyErr error
	switch mode {
	case modeExact:
		out, applyErr = resolveExact(string(data), editBenchFile, c.Old, c.New)
	case modeRepair:
		out, applyErr = resolveRepair(string(data), editBenchFile, c.Old, c.New)
	case modeHashline:
		anchorHash := c.AnchorHash
		if anchorHash == "" {
			// The model supplies the canonical hash when the fixture omits it;
			// a non-empty AnchorHash is used verbatim so stale-hash cases can be
			// expressed (see hashline-wrong-hash-rejected).
			anchorHash = hashlineHash(c.Anchor)
		}
		out, applyErr = resolveHashline(string(data), editBenchFile, c.Anchor, anchorHash, c.Old, c.New)
	default:
		res.Reason = "unknown mode " + strconv.Quote(mode)
		return res
	}

	final := string(data)
	if applyErr == nil {
		if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
			res.Reason = "cannot write edited file: " + err.Error()
			return res
		}
		edited, err := os.ReadFile(path)
		if err != nil {
			res.Reason = "cannot read edited file: " + err.Error()
			return res
		}
		final = string(edited)
	}

	passed, got, reason := gradeEditCase(c, final, applyErr)
	res.Passed, res.Got, res.Reason = passed, got, reason
	return res
}

// RunEditBench runs every case and aggregates the outcome. Order is preserved
// and no global state is touched, so repeated calls are identical.
func RunEditBench(cases []EditCase) EditBenchReport {
	report := EditBenchReport{ByMode: make(map[string]ModeStat)}
	for _, c := range cases {
		report.Total++
		result := RunEditCase(c)
		stat := report.ByMode[result.Mode]
		if result.Passed {
			report.Passed++
			stat.Passed++
		} else {
			report.Failed++
			stat.Failed++
			report.Failures = append(report.Failures, result)
		}
		report.ByMode[result.Mode] = stat
	}
	return report
}

// String renders a compact, deterministic summary with a pass rate per mode.
func (r EditBenchReport) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "editbench: %d/%d passed (%.1f%%)", r.Passed, r.Total, passRate(r.Passed, r.Total))
	modes := make([]string, 0, len(r.ByMode))
	for mode := range r.ByMode {
		modes = append(modes, mode)
	}
	sort.Strings(modes)
	for _, mode := range modes {
		stat := r.ByMode[mode]
		total := stat.Passed + stat.Failed
		fmt.Fprintf(&b, " | %s %d/%d (%.1f%%)", mode, stat.Passed, total, passRate(stat.Passed, total))
	}
	return b.String()
}

func passRate(passed, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(passed) * 100 / float64(total)
}

// buildCurrentContent simulates edits made to the file after the model read it.
// ShiftLines is inserted immediately before the match target so line numbers
// shift while the target text itself stays intact (content anchoring must win).
func buildCurrentContent(c EditCase) string {
	if c.ShiftLines == "" {
		return c.Original
	}
	target := c.Old
	if c.Mode == modeHashline && c.Anchor != "" {
		target = c.Anchor
	}
	idx := strings.Index(c.Original, target)
	if idx < 0 {
		return c.ShiftLines + c.Original
	}
	return c.Original[:idx] + c.ShiftLines + c.Original[idx:]
}

// gradeEditCase compares the observed outcome with the declared expectation.
func gradeEditCase(c EditCase, final string, err error) (bool, string, string) {
	gotErr := ""
	if err != nil {
		gotErr = editbenchOneLine(err.Error())
	}
	if c.WantErrContains != "" {
		if err == nil {
			return false, "success: " + editbenchClip(final), "expected an error containing " + strconv.Quote(c.WantErrContains) + ", but the edit succeeded"
		}
		if !strings.Contains(err.Error(), c.WantErrContains) {
			return false, "error: " + gotErr, "error does not contain " + strconv.Quote(c.WantErrContains)
		}
		return true, "error: " + gotErr, ""
	}
	if err != nil {
		return false, "error: " + gotErr, "expected success, but the edit failed: " + gotErr
	}
	if c.WantResult != "" && !strings.Contains(final, c.WantResult) {
		return false, "success: " + editbenchClip(final), "result does not contain " + strconv.Quote(c.WantResult)
	}
	return true, "success: " + editbenchClip(final), ""
}

func editbenchOneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func editbenchClip(s string) string {
	joined := editbenchOneLine(s)
	if len(joined) <= 160 {
		return joined
	}
	return joined[:159] + "…"
}

// ---- resolution contract, mirrored from internal/tools ----

// hashlineHash is the first 12 lowercase hex chars of sha256(s), matching the
// tool's published contract byte for byte.
func hashlineHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}

// resolveExact mirrors the default edit branch: a unique literal match wins.
func resolveExact(content, path, old, newText string) (string, error) {
	count := strings.Count(content, old)
	switch count {
	case 0:
		return "", fmt.Errorf("edit: old text not found in %s", path)
	case 1:
		return strings.Replace(content, old, newText, 1), nil
	default:
		return "", fmt.Errorf("edit: old text appears %d times in %s (not unique); a unique match is required", count, path)
	}
}

// resolveRepair mirrors the repair=true branch: exact, then the graded ladder.
func resolveRepair(content, path, old, newText string) (string, error) {
	var out string
	var ok bool
	switch strings.Count(content, old) {
	case 1:
		out, ok = strings.Replace(content, old, newText, 1), true
	case 0:
		out, ok = repairReplace(content, old, newText)
	default:
		ok = false // a literal that occurs twice is never guessed at
	}
	if !ok {
		return "", editRepairError(content, old)
	}
	return out, nil
}

// resolveHashline mirrors resolveHashlineEdit: verify the hash, then match the
// anchor by content, retrying the ladder only when the anchor is absent.
func resolveHashline(content, path, anchor, anchorHash, old, newText string) (string, error) {
	if anchorHash != "" {
		want := hashlineHash(anchor)
		if !strings.EqualFold(anchorHash, want) {
			return "", fmt.Errorf("edit: stale anchor: content changed since it was read (expected hash %s, got %s)", want, anchorHash)
		}
	}
	oldText := anchor
	if oldText == "" {
		oldText = old
	}
	switch strings.Count(content, oldText) {
	case 1:
		return strings.Replace(content, oldText, newText, 1), nil
	case 0:
		// fall through to the repair ladder for stale-anchor recovery
	default:
		return "", fmt.Errorf("edit: stale anchor appears %d times in %s (not unique); file unchanged", strings.Count(content, oldText), path)
	}
	if out, ok := repairReplace(content, oldText, newText); ok {
		return out, nil
	}
	msg := "edit: stale anchor no longer present in " + path + "; file unchanged"
	if s := formatSimilarLines(content, oldText); s != "" {
		msg += "; " + s
	}
	return "", errors.New(msg)
}

// repairReplace walks the Aider-style ladder in order, refusing to guess when
// a literal match is ambiguous.
func repairReplace(content, old, newText string) (string, bool) {
	switch strings.Count(content, old) {
	case 1:
		return strings.Replace(content, old, newText, 1), true
	case 0:
	default:
		return "", false
	}
	if out, ok := repairIndent(content, old, newText); ok {
		return out, true
	}
	if out, ok := repairBlankLine(content, old, newText); ok {
		return out, true
	}
	if out, ok := repairElision(content, old, newText); ok {
		return out, true
	}
	return "", false
}

func commonIndent(lines []string) string {
	indent := ""
	seen := false
	for _, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		lead := ln[:len(ln)-len(strings.TrimLeft(ln, " \t"))]
		if !seen {
			indent, seen = lead, true
			continue
		}
		i := 0
		for i < len(indent) && i < len(lead) && indent[i] == lead[i] {
			i++
		}
		indent = indent[:i]
	}
	return indent
}

func stripIndent(lines []string, indent string) []string {
	out := make([]string, len(lines))
	for i, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			out[i] = ""
			continue
		}
		out[i] = strings.TrimPrefix(ln, indent)
	}
	return out
}

func indentLines(lines []string, indent string) []string {
	out := make([]string, len(lines))
	for i, ln := range lines {
		if ln == "" {
			out[i] = ""
		} else {
			out[i] = indent + ln
		}
	}
	return out
}

func equalLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func repairIndent(content, old, newText string) (string, bool) {
	oldLines := strings.Split(old, "\n")
	strippedOld := stripIndent(oldLines, commonIndent(oldLines))
	newLines := strings.Split(newText, "\n")
	strippedNew := stripIndent(newLines, commonIndent(newLines))

	contentLines := strings.Split(content, "\n")
	width := len(strippedOld)
	if width == 0 || len(contentLines) < width {
		return "", false
	}

	start := -1
	fileIndent := ""
	for i := 0; i+width <= len(contentLines); i++ {
		window := contentLines[i : i+width]
		indent := commonIndent(window)
		if equalLines(stripIndent(window, indent), strippedOld) {
			if start >= 0 {
				return "", false // two candidate blocks: ambiguous
			}
			start, fileIndent = i, indent
		}
	}
	if start < 0 {
		return "", false
	}

	repl := indentLines(strippedNew, fileIndent)
	out := make([]string, 0, len(contentLines)-width+len(repl))
	out = append(out, contentLines[:start]...)
	out = append(out, repl...)
	out = append(out, contentLines[start+width:]...)
	return strings.Join(out, "\n"), true
}

func repairBlankLine(content, old, newText string) (string, bool) {
	type pair struct{ old, newText string }
	var pairs []pair
	if v, ok := dropLeadingBlankLine(old); ok {
		nv := newText
		if x, ok2 := dropLeadingBlankLine(newText); ok2 {
			nv = x
		}
		pairs = append(pairs, pair{v, nv})
	}
	if v, ok := dropTrailingBlankLine(old); ok {
		nv := newText
		if x, ok2 := dropTrailingBlankLine(newText); ok2 {
			nv = x
		}
		pairs = append(pairs, pair{v, nv})
	}
	for _, p := range pairs {
		if strings.Count(content, p.old) == 1 {
			return strings.Replace(content, p.old, p.newText, 1), true
		}
	}
	return "", false
}

func dropLeadingBlankLine(s string) (string, bool) {
	lines := strings.Split(s, "\n")
	if len(lines) > 1 && strings.TrimSpace(lines[0]) == "" {
		return strings.Join(lines[1:], "\n"), true
	}
	return s, false
}

func dropTrailingBlankLine(s string) (string, bool) {
	lines := strings.Split(s, "\n")
	last := len(lines) - 1
	if last > 0 && strings.TrimSpace(lines[last]) == "" {
		return strings.Join(lines[:last], "\n"), true
	}
	return s, false
}

func splitElision(s string) ([]string, bool) {
	lines := strings.Split(s, "\n")
	pieces := []string{}
	cur := []string{}
	markers := 0
	for _, ln := range lines {
		if strings.TrimSpace(ln) == "..." {
			pieces = append(pieces, strings.Join(cur, "\n"))
			cur = cur[:0]
			markers++
			continue
		}
		cur = append(cur, ln)
	}
	pieces = append(pieces, strings.Join(cur, "\n"))
	return pieces, markers > 0
}

func repairElision(content, old, newText string) (string, bool) {
	oldPieces, hasOldMarkers := splitElision(old)
	newPieces, hasNewMarkers := splitElision(newText)
	if !hasOldMarkers || !hasNewMarkers || len(oldPieces) != len(newPieces) {
		return "", false
	}

	var b strings.Builder
	cursor := 0
	for i, piece := range oldPieces {
		if strings.TrimSpace(piece) == "" {
			b.WriteString(newPieces[i])
			continue
		}
		if strings.Count(content, piece) != 1 {
			return "", false
		}
		rel := strings.Index(content[cursor:], piece)
		if rel < 0 {
			return "", false
		}
		at := cursor + rel
		b.WriteString(content[cursor:at])
		b.WriteString(newPieces[i])
		cursor = at + len(piece)
	}
	b.WriteString(content[cursor:])
	return b.String(), true
}

func editRepairError(content, old string) error {
	msg := "edit repair failed: no unique match after 1 edit(s) attempted; file unchanged"
	if s := formatSimilarLines(content, old); s != "" {
		msg += "; " + s
	}
	return errors.New(msg)
}

// The diagnostic helpers below mirror the tool's deterministic similar-line
// scoring so error text stays faithful; they do not affect match decisions.
func similarLines(content, sample string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	type scored struct {
		line  string
		score int
	}
	var candidates []scored
	for _, ln := range strings.Split(content, "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		if score := similarityScore(sample, ln); score > 0 {
			candidates = append(candidates, scored{ln, score})
		}
	}
	if len(candidates) == 0 {
		for _, ln := range strings.Split(content, "\n") {
			if strings.TrimSpace(ln) == "" {
				continue
			}
			candidates = append(candidates, scored{ln, 0})
			if len(candidates) == limit {
				break
			}
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	out := make([]string, 0, len(candidates))
	for _, c := range candidates {
		out = append(out, c.line)
	}
	return out
}

func similarityScore(sample, line string) int {
	a, b := strings.ToLower(sample), strings.ToLower(line)
	return commonMultiset(strings.Fields(a), strings.Fields(b))*8 + commonRunes([]rune(a), []rune(b))
}

func commonMultiset(a, b []string) int {
	counts := make(map[string]int, len(a))
	for _, v := range a {
		counts[v]++
	}
	n := 0
	for _, v := range b {
		if counts[v] > 0 {
			counts[v]--
			n++
		}
	}
	return n
}

func commonRunes(a, b []rune) int {
	counts := make(map[rune]int, len(a))
	for _, r := range a {
		counts[r]++
	}
	n := 0
	for _, r := range b {
		if counts[r] > 0 {
			counts[r]--
			n++
		}
	}
	return n
}

func formatSimilarLines(content, sample string) string {
	lines := similarLines(content, sample, 5)
	if len(lines) == 0 {
		return ""
	}
	quoted := make([]string, 0, len(lines))
	for _, ln := range lines {
		quoted = append(quoted, strconv.Quote(ln))
	}
	return "most similar lines: " + strings.Join(quoted, " | ")
}
