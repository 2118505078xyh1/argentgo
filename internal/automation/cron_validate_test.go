package automation

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestValidateCronValidExpressions(t *testing.T) {
	cases := []struct {
		name string
		expr string
	}{
		{"every minute", "* * * * *"},
		{"quarter hourly", "*/15 * * * *"},
		{"fixed time", "0 3 * * *"},
		{"weekday range", "30 9 * * 1-5"},
		{"sunday as seven", "0 0 * * 7"},
		{"sunday as zero", "0 0 * * 0"},
		{"stepped range", "0-30/10 * * * *"},
		{"value with step", "5/15 * * * *"},
		{"lists", "0,30 9-17 1,15 6,12 1-5"},
		{"both dom and dow restricted", "0 0 13 * 5"},
		{"surrounding whitespace", "  0\t3  *  * *  "},
		{"unicode whitespace", "0\u30003\u3000*\u3000*\u3000*"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issues := ValidateCron(tc.expr)
			if issues == nil {
				t.Fatal("ValidateCron must return a non-nil slice")
			}
			if len(issues) != 0 {
				t.Fatalf("ValidateCron(%q) = %v, want no issues", tc.expr, issues)
			}
		})
	}
}

func TestValidateCronCollectsAllIssues(t *testing.T) {
	cases := []struct {
		name     string
		expr     string
		wantErrs int
		fields   []int
	}{
		{"ranges across fields", "60 24 * * 9", 3, []int{0, 1, 4}},
		{"one error per field", "*/0 99 5-1 13 MON", 5, []int{0, 1, 2, 3, 4}},
		{"multiple list elements", "1,,2 * * * 8,a", 3, []int{0, 4, 4}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issues := ValidateCron(tc.expr)
			var gotFields []int
			for _, issue := range issues {
				if issue.Severity != cronSeverityError {
					continue
				}
				gotFields = append(gotFields, issue.Field)
			}
			if len(gotFields) != tc.wantErrs {
				t.Fatalf("ValidateCron(%q) errors = %v, want %d (%v)", tc.expr, issues, tc.wantErrs, issues)
			}
			if !reflect.DeepEqual(gotFields, tc.fields) {
				t.Fatalf("ValidateCron(%q) error fields = %v, want %v", tc.expr, gotFields, tc.fields)
			}
		})
	}
}

func TestValidateCronFieldCount(t *testing.T) {
	cases := []struct {
		name string
		expr string
		want int
	}{
		{"empty", "", 0},
		{"four fields", "* * * *", 4},
		{"six fields", "* * * * * *", 6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issues := ValidateCron(tc.expr)
			if len(issues) == 0 || issues[0].Field != -1 {
				t.Fatalf("ValidateCron(%q) = %v, want a field-count issue", tc.expr, issues)
			}
			if !strings.Contains(issues[0].Message, "5 fields") ||
				!strings.Contains(issues[0].Message, "got "+strconv.Itoa(tc.want)) {
				t.Fatalf("field-count message = %q", issues[0].Message)
			}
		})
	}
}

func TestValidateCronRangesAndSteps(t *testing.T) {
	cases := []struct {
		name    string
		expr    string
		message string
	}{
		{"minute too high", "60 * * * *", "outside 0-59"},
		{"hour too high", "* 24 * * *", "outside 0-23"},
		{"day of month zero", "* * 0 * *", "outside 1-31"},
		{"month too high", "* * * 13 *", "outside 1-12"},
		{"weekday too high", "* * * * 8", "outside 0-7"},
		{"zero step", "*/0 * * * *", "positive integer"},
		{"negative step", "*/-1 * * * *", "positive integer"},
		{"reversed range", "5-1 * * * *", "reversed range"},
		{"repeated separator", "*/2/3 * * * *", "at most one"},
		{"malformed range", "1-2-3 * * * *", "malformed range"},
		{"non numeric", "a * * * *", "not a number"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issues := ValidateCron(tc.expr)
			if len(issues) == 0 {
				t.Fatalf("ValidateCron(%q) should report an issue", tc.expr)
			}
			found := false
			for _, issue := range issues {
				if strings.Contains(issue.Message, tc.message) {
					found = true
				}
			}
			if !found {
				t.Fatalf("ValidateCron(%q) = %v, want a message containing %q", tc.expr, issues, tc.message)
			}
		})
	}
}

func TestValidateCronWeekdayAliases(t *testing.T) {
	cases := []struct {
		name  string
		expr  string
		alias string
		value int
	}{
		{"mon", "0 0 * * MON", "MON", 1},
		{"sunday", "0 0 * * sunday", "sunday", 0},
		{"friday list", "0 0 * * 1,fri", "fri", 5},
		{"alias range", "0 0 * * mon-fri", "mon", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issues := ValidateCron(tc.expr)
			want := "weekday alias"
			found := false
			for _, issue := range issues {
				if strings.Contains(issue.Message, want) &&
					strings.Contains(issue.Message, "use "+strconv.Itoa(tc.value)) &&
					strings.EqualFold(issue.Text, tc.alias) {
					found = true
				}
			}
			if !found {
				t.Fatalf("ValidateCron(%q) = %v, want alias diagnostic for %q -> %d", tc.expr, issues, tc.alias, tc.value)
			}
		})
	}
}

func TestValidateCronNeverFiresWarning(t *testing.T) {
	cases := []struct {
		name    string
		expr    string
		warning bool
	}{
		{"31 february", "0 0 31 2 *", true},
		{"valid, no warning", "0 3 * * *", false},
		{"leap day can fire", "0 0 29 2 *", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issues := ValidateCron(tc.expr)
			if len(issues) != 0 && issues[0].Severity != cronSeverityWarning {
				t.Fatalf("ValidateCron(%q) = %v, want no errors", tc.expr, issues)
			}
			hasWarning := len(issues) == 1 && issues[0].Severity == cronSeverityWarning
			if hasWarning != tc.warning {
				t.Fatalf("ValidateCron(%q) warning = %v, want %v (%v)", tc.expr, hasWarning, tc.warning, issues)
			}
		})
	}
}

func TestValidateCronCJKIndependent(t *testing.T) {
	// A multibyte (CJK) field before the bad value must not corrupt byte
	// offsets, and the same expression must always yield the same diagnostics.
	expr := "每 3 * * 8"
	first := ValidateCron(expr)
	if len(first) != 2 {
		t.Fatalf("ValidateCron(%q) = %v, want 2 issues", expr, first)
	}
	if first[0].Start != 0 || first[0].Text != "每" {
		t.Fatalf("first issue = %+v, want byte 0 / %q", first[0], "每")
	}
	// "每" is 3 bytes, space at 3, "3" at 4, space at 5, "*" at 6, space at 7,
	// "*" at 8, space at 9, "8" at 10.
	if first[1].Start != 10 || first[1].Text != "8" {
		t.Fatalf("second issue = %+v, want byte 10 / %q", first[1], "8")
	}
	for _, issue := range first {
		for _, r := range issue.Message {
			if r > 127 {
				t.Fatalf("diagnostic message must be locale-independent English, got %q", issue.Message)
			}
		}
	}
	second := ValidateCron(expr)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("ValidateCron is not deterministic: %v vs %v", first, second)
	}
	// A CJK-dominated 6-field expression reports the field-count issue first.
	if issues := ValidateCron("0 3 * * * 每天"); len(issues) == 0 || issues[0].Field != -1 {
		t.Fatalf("CJK extra field = %v, want a field-count issue", issues)
	}
}

func TestValidateCronIssueString(t *testing.T) {
	issues := ValidateCron("*/0 3 * * 9")
	if len(issues) != 2 {
		t.Fatalf("ValidateCron = %v, want 2 issues", issues)
	}
	got := issues[0].String()
	for _, want := range []string{
		"field 1 (minute)",
		"at byte 2",
		"step must be a positive integer",
		"error",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("issue string %q does not contain %q", got, want)
		}
	}
}
