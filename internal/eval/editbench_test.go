package eval

import (
	"path/filepath"
	"strings"
	"testing"
)

const editBenchFixture = "../../evals/edit_bench.json"

func loadBenchFixture(t *testing.T) []EditCase {
	t.Helper()
	cases, err := LoadEditCases(editBenchFixture)
	if err != nil {
		t.Fatalf("LoadEditCases(%q): %v", editBenchFixture, err)
	}
	return cases
}

// TestLoadEditCasesParsesFixture checks that the fixture decodes into well-
// formed cases: a unique ID, a known mode, and at least one expectation.
func TestLoadEditCasesParsesFixture(t *testing.T) {
	cases := loadBenchFixture(t)
	if len(cases) < 20 {
		t.Fatalf("fixture has %d cases, want >= 20", len(cases))
	}
	seen := make(map[string]bool, len(cases))
	modes := make(map[string]bool)
	for i, c := range cases {
		if c.ID == "" {
			t.Errorf("case %d has an empty ID", i)
		}
		if seen[c.ID] {
			t.Errorf("case %d repeats ID %q", i, c.ID)
		}
		seen[c.ID] = true
		switch c.Mode {
		case modeExact, modeRepair, modeHashline:
		default:
			t.Errorf("case %s has invalid mode %q", c.ID, c.Mode)
		}
		modes[c.Mode] = true
		if c.WantResult == "" && c.WantErrContains == "" {
			t.Errorf("case %s declares no expectation", c.ID)
		}
		if strings.Contains(c.WantResult, "WantResult") || strings.Contains(c.WantErrContains, "WantErr") {
			t.Errorf("case %s contains a placeholder expectation", c.ID)
		}
	}
	for _, mode := range []string{modeExact, modeRepair, modeHashline} {
		if !modes[mode] {
			t.Errorf("fixture does not cover mode %q", mode)
		}
	}
}

// TestEditBenchFixtureCoversScenarios guards the benchmark's breadth: it must
// exercise both successful and rejected edits in every mode, plus UTF-8.
func TestEditBenchFixtureCoversScenarios(t *testing.T) {
	cases := loadBenchFixture(t)
	var success, failure, cjk, shifted int
	for _, c := range cases {
		if c.WantErrContains != "" {
			failure++
		} else {
			success++
		}
		if strings.ContainsAny(c.Original+c.Old+c.New+c.Anchor, "你好世界函数方法设置值中文锚点插入标题第一步第二步结束完成返回") {
			cjk++
		}
		if c.ShiftLines != "" {
			shifted++
		}
	}
	if success == 0 || failure == 0 {
		t.Fatalf("fixture must contain both success and failure cases (got %d success, %d failure)", success, failure)
	}
	if cjk < 4 {
		t.Errorf("fixture has only %d CJK/UTF-8 cases, want >= 4", cjk)
	}
	if shifted < 2 {
		t.Errorf("fixture has only %d shifted-anchor cases, want >= 2", shifted)
	}
}

// TestRunEditBenchMatchesFixtureExpectations is the core assertion: for every
// case the harness must report that the algorithm behaved as the fixture says
// it should. A harness that always passes would not detect a regression.
func TestRunEditBenchMatchesFixtureExpectations(t *testing.T) {
	cases := loadBenchFixture(t)
	report := RunEditBench(cases)
	t.Logf("ACTUAL BENCHMARK NUMBERS: %s", report)
	if report.Total != len(cases) {
		t.Fatalf("report.Total = %d, want %d", report.Total, len(cases))
	}
	if len(report.Failures) != report.Failed {
		t.Fatalf("len(Failures) = %d, report.Failed = %d", len(report.Failures), report.Failed)
	}
	for _, failure := range report.Failures {
		t.Errorf("case %s (%s, %s) did not match expectation: got %s; reason: %s",
			failure.ID, failure.Name, failure.Mode, failure.Got, failure.Reason)
	}
	if report.Passed != len(cases) {
		t.Fatalf("pass rate %d/%d; %d cases disagreed with the fixture", report.Passed, len(cases), report.Failed)
	}
}

// TestRunEditBenchByModeAggregation verifies that ByMode counts add up to the
// totals and match an independent tally of the fixture.
func TestRunEditBenchByModeAggregation(t *testing.T) {
	cases := loadBenchFixture(t)
	want := make(map[string]int)
	for _, c := range cases {
		want[c.Mode]++
	}
	report := RunEditBench(cases)
	if len(report.ByMode) != len(want) {
		t.Fatalf("ByMode has %d entries, want %d: %v", len(report.ByMode), len(want), report.ByMode)
	}
	sumPassed, sumFailed := 0, 0
	for mode, total := range want {
		stat, ok := report.ByMode[mode]
		if !ok {
			t.Fatalf("ByMode is missing mode %q", mode)
		}
		if got := stat.Passed + stat.Failed; got != total {
			t.Errorf("mode %s: Passed+Failed = %d, want %d", mode, got, total)
		}
		sumPassed += stat.Passed
		sumFailed += stat.Failed
	}
	if sumPassed != report.Passed || sumFailed != report.Failed {
		t.Errorf("ByMode sums (%d passed, %d failed) disagree with report (%d, %d)",
			sumPassed, sumFailed, report.Passed, report.Failed)
	}
	if report.Passed+report.Failed != report.Total {
		t.Errorf("Passed+Failed = %d, Total = %d", report.Passed+report.Failed, report.Total)
	}
}

// TestEditBenchReportString checks that the summary carries a pass rate for
// every mode and is stable across calls.
func TestEditBenchReportString(t *testing.T) {
	cases := loadBenchFixture(t)
	report := RunEditBench(cases)
	s := report.String()
	if !strings.Contains(s, "editbench:") || !strings.Contains(s, "passed") {
		t.Fatalf("summary missing totals: %q", s)
	}
	for _, mode := range []string{modeExact, modeRepair, modeHashline} {
		if !strings.Contains(s, mode) {
			t.Errorf("summary missing mode %q: %q", mode, s)
		}
	}
	if strings.Count(s, "100.0%") == 0 {
		t.Errorf("summary has no per-mode pass rate: %q", s)
	}
	if again := RunEditBench(cases).String(); again != s {
		t.Errorf("summary is not deterministic:\n%s\n%s", s, again)
	}
}

// TestHashlineHashMatchesSHA256 pins the hash contract to an independent known
// digest: sha256("hello") = 2cf24dba5fb0a30e... -> first 12 = 2cf24dba5fb0.
func TestHashlineHashMatchesSHA256(t *testing.T) {
	if got, want := hashlineHash("hello"), "2cf24dba5fb0"; got != want {
		t.Fatalf("hashlineHash(%q) = %q, want %q", "hello", got, want)
	}
	if h := hashlineHash(""); len(h) != 12 {
		t.Fatalf("hashlineHash(\"\") length = %d, want 12", len(h))
	}
	if hashlineHash("hello") == hashlineHash("hello!") {
		t.Fatal("different content produced the same hash")
	}
}

// TestRunEditCaseDetectsMismatch proves the grader is not vacuous: it must
// report failure when the observed outcome contradicts the declared
// expectation, in both directions (expected success vs. expected error).
func TestRunEditCaseDetectsMismatch(t *testing.T) {
	cases := []struct {
		name string
		tc   EditCase
		want bool
	}{
		{
			name: "expected success and got success",
			tc:   EditCase{ID: "ok-success", Mode: modeExact, Original: "abc\n", Old: "abc", New: "xyz", WantResult: "xyz"},
			want: true,
		},
		{
			name: "expected error and got matching error",
			tc:   EditCase{ID: "ok-error", Mode: modeExact, Original: "abc\n", Old: "missing", New: "x", WantErrContains: "not found"},
			want: true,
		},
		{
			name: "succeeded but expectation said error",
			tc:   EditCase{ID: "bad-success", Mode: modeExact, Original: "abc\n", Old: "abc", New: "x", WantErrContains: "not found"},
			want: false,
		},
		{
			name: "failed but expectation said success",
			tc:   EditCase{ID: "bad-error", Mode: modeExact, Original: "abc\n", Old: "missing", New: "x", WantResult: "abc"},
			want: false,
		},
		{
			name: "succeeded with wrong content",
			tc:   EditCase{ID: "bad-content", Mode: modeExact, Original: "abc\n", Old: "abc", New: "xyz", WantResult: "NOT_PRESENT"},
			want: false,
		},
		{
			name: "failed with a different error",
			tc:   EditCase{ID: "bad-error-text", Mode: modeExact, Original: "abc\n", Old: "missing", New: "x", WantErrContains: "not unique"},
			want: false,
		},
		{
			name: "unknown mode",
			tc:   EditCase{ID: "bad-mode", Mode: "bogus", Original: "abc\n", Old: "abc", New: "x", WantResult: "x"},
			want: false,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := RunEditCase(tt.tc)
			if got.Passed != tt.want {
				t.Fatalf("Passed = %t, want %t (got=%q reason=%q)", got.Passed, tt.want, got.Got, got.Reason)
			}
			if tt.want && got.Reason != "" {
				t.Fatalf("passing case has a reason: %q", got.Reason)
			}
			if !tt.want && got.Reason == "" {
				t.Fatal("failing case has no reason")
			}
		})
	}
}

// TestLoadEditCasesErrors confirms malformed documents are rejected rather
// than silently graded as all-pass.
func TestLoadEditCasesErrors(t *testing.T) {
	for _, doc := range []string{
		`{"cases":[]}`,
		`{"cases":[{"id":"a","mode":"nope","original":"x","new":"y","want_result":"y"}]}`,
		`{"cases":[{"id":"a","mode":"exact","original":"x","new":"y","want_result":"y"},{"id":"a","mode":"exact","original":"x","new":"y","want_result":"y"}]}`,
		`{"cases":[{"id":"a","mode":"exact","original":"x","new":"y"}]}`,
	} {
		if _, err := ParseEditCases([]byte(doc)); err == nil {
			t.Errorf("ParseEditCases accepted invalid document: %s", doc)
		}
	}
	// A bare array is accepted too.
	cases, err := ParseEditCases([]byte(`[{"id":"a","mode":"exact","original":"x","old":"x","new":"y","want_result":"y"}]`))
	if err != nil {
		t.Fatalf("bare array rejected: %v", err)
	}
	if len(cases) != 1 || cases[0].ID != "a" {
		t.Fatalf("bare array parsed as %+v", cases)
	}
}

// TestEditBenchFixturePath ensures the documented fixture location resolves
// from the package directory, where `go test` runs.
func TestEditBenchFixturePath(t *testing.T) {
	if _, err := filepath.Abs(editBenchFixture); err != nil {
		t.Fatal(err)
	}
	loadBenchFixture(t)
}
