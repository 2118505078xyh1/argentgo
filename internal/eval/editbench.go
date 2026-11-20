// Package eval's edit benchmark measures the REAL built-in edit tool, end to
// end, against model-shaped edit scenarios.
//
// Methodology
//
// Each case declares the bytes a model saw (Original), the arguments it would
// send (Old/New/Anchor/AnchorHash/Repair/Hashline), and the expected outcome.
// RunEditCase seeds a scratch file under a temp workspace, builds the JSON
// argument object the tool's schema accepts, and calls tools.Registry.Run with
// the name "edit". That drives the shipped code path in full: argument decoding
// and the old/old_text aliases, workspace path resolution, the mutation lock,
// the default / repair / hashline resolution branches, and the final
// os.WriteFile. The resulting file bytes and the returned error are graded
// against the case's declared expectation.
//
// This is deliberately NOT a reimplementation of the resolution helpers. An
// earlier version of this benchmark mirrored internal/tools' unexported helpers
// line for line; it could only prove the mirror agreed with itself, and its
// report honestly flagged "100% is a regression guard, not the real tool path".
// The registry.Run path below measures the code users actually execute.
//
// What it does NOT measure: the agent loop, model/network calls, or real
// policy/approval wiring. The harness constructs a yolo registry so the
// mutating tool runs; a test exercises the yolo=false mutation gate as a
// signature proving the registry path is used. It is fully offline and
// deterministic.
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
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/tools"
)

// Edit modes understood by the benchmark.
const (
	modeExact    = "exact"
	modeRepair   = "repair"
	modeHashline = "hashline"
)

// editBenchFile is the scratch file every case is applied to. It is passed to
// the tool as a relative path, so the tool's diagnostics name it verbatim and
// the fixtures' error needles stay stable.
const editBenchFile = "edit.txt"

// EditCase is one scripted edit scenario. Original is the file content the
// model saw; Anchor is the exact text it hashed for hashline mode; AnchorHash is
// the model-supplied hash (empty means "do not send anchor_hash", which makes
// the real tool anchor by content); ShiftLines is text inserted before the
// match target to simulate lines added above it between read and write.
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

// EditCaseResult is the graded outcome of one case. Passed means the real tool
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

// editToolArgs is the edit tool's JSON schema. omitempty keeps undeclared
// fields off the wire, so the harness sends exactly the arguments the fixture
// describes; the mode fields are derived from the case's mode.
type editToolArgs struct {
	Path       string `json:"path"`
	Old        string `json:"old,omitempty"`
	New        string `json:"new,omitempty"`
	Repair     bool   `json:"repair,omitempty"`
	Hashline   bool   `json:"hashline,omitempty"`
	Anchor     string `json:"anchor,omitempty"`
	AnchorHash string `json:"anchor_hash,omitempty"`
}

// editArgs renders the case as the arguments the shipped edit tool expects.
func editArgs(c EditCase) (string, error) {
	args := editToolArgs{
		Path:       editBenchFile,
		Old:        c.Old,
		New:        c.New,
		Anchor:     c.Anchor,
		AnchorHash: c.AnchorHash,
	}
	switch c.Mode {
	case modeRepair:
		args.Repair = true
	case modeHashline:
		args.Hashline = true
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return "", fmt.Errorf("encode edit arguments: %w", err)
	}
	return string(raw), nil
}

// seedEditFile writes the bytes the tool should see into the scratch workspace.
func seedEditFile(root string, c EditCase) (string, error) {
	path := filepath.Join(root, editBenchFile)
	if err := os.WriteFile(path, []byte(buildCurrentContent(c)), 0o644); err != nil {
		return "", fmt.Errorf("cannot seed scratch file: %w", err)
	}
	return path, nil
}

// runEditViaRegistry invokes the shipped edit tool through the public registry.
// With yolo=true the mutating tool runs; with false the registry rejects it
// before the tool is reached (used by a test as a real-path signature).
func runEditViaRegistry(root string, c EditCase, yolo bool) (string, error) {
	args, err := editArgs(c)
	if err != nil {
		return "", err
	}
	reg := tools.New(root, yolo)
	return reg.Run(context.Background(), "edit", args)
}

// RunEditCase applies one case through the real edit tool and grades the result.
// All I/O is confined to a MkdirTemp scratch directory that is removed before
// returning.
func RunEditCase(c EditCase) EditCaseResult {
	mode := c.Mode
	if mode == "" {
		mode = modeExact
	}
	res := EditCaseResult{ID: c.ID, Name: c.Name, Mode: mode}
	if mode != modeExact && mode != modeRepair && mode != modeHashline {
		res.Reason = "unknown mode " + strconv.Quote(mode)
		return res
	}

	dir, err := os.MkdirTemp("", "editbench-")
	if err != nil {
		res.Reason = "cannot create scratch directory: " + err.Error()
		return res
	}
	defer os.RemoveAll(dir)

	path, err := seedEditFile(dir, c)
	if err != nil {
		res.Reason = err.Error()
		return res
	}

	_, applyErr := runEditViaRegistry(dir, c, true)
	final := ""
	if applyErr == nil {
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
