package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeDoctorLSPDiagnosticsSnapshot(t *testing.T, dir string, snapshot doctorLSPDiagnosticsSnapshot) {
	t.Helper()
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, doctorLSPDiagnosticsReportFile), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func doctorLSPDiagnosticsTestApp(dir string, optIn bool) App {
	env := map[string]string{}
	if optIn {
		env[doctorLSPDiagnosticsEnv] = "1"
	}
	ioState, _, _ := newTestIO("", env)
	return App{IO: ioState, StorageDir: dir}
}

func TestDoctorLSPDiagnosticsIssuesFromSnapshot(t *testing.T) {
	dir := t.TempDir()
	writeDoctorLSPDiagnosticsSnapshot(t, dir, doctorLSPDiagnosticsSnapshot{
		Reports: []doctorLSPDiagnosticsReport{
			{
				URI: "file:///z.go",
				Diagnostics: []doctorLSPDiagnostic{
					{Severity: "error", Message: "boom"},
					{Severity: "warning", Message: "careful"},
				},
			},
			{
				URI: "file:///a.go",
				Diagnostics: []doctorLSPDiagnostic{
					{Severity: "information", Message: "hint"},
				},
			},
		},
	})

	issues := doctorLSPDiagnosticsIssues(doctorLSPDiagnosticsTestApp(dir, true))
	if len(issues) != 3 {
		t.Fatalf("issues = %d, want 3: %+v", len(issues), issues)
	}
	// 按 URI 排序：a.go 的两条在前，z.go 的在后。
	for _, want := range []string{"file:///a.go", "file:///z.go", "file:///z.go"} {
		found := false
		for _, issue := range issues {
			if strings.Contains(issue.Message, want) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("issues missing %q: %+v", want, issues)
		}
	}
	for _, issue := range issues {
		if issue.Name != doctorLSPDiagnosticsIssueName || issue.Level != statusWarn {
			t.Fatalf("unexpected issue shape: %+v", issue)
		}
		if !strings.Contains(issue.Message, "count=") || !strings.Contains(issue.Message, "severity=") {
			t.Fatalf("issue missing severity/count: %+v", issue)
		}
	}
	if !strings.Contains(issues[0].Message, "count=1") || !strings.Contains(issues[1].Message, "count=2") {
		t.Fatalf("count does not reflect per-report diagnostics: %+v", issues)
	}
}

func TestDoctorLSPDiagnosticsIssuesRequiresOptIn(t *testing.T) {
	dir := t.TempDir()
	writeDoctorLSPDiagnosticsSnapshot(t, dir, doctorLSPDiagnosticsSnapshot{
		Reports: []doctorLSPDiagnosticsReport{{
			URI:         "file:///a.go",
			Diagnostics: []doctorLSPDiagnostic{{Severity: "error", Message: "boom"}},
		}},
	})
	if got := doctorLSPDiagnosticsIssues(doctorLSPDiagnosticsTestApp(dir, false)); got != nil {
		t.Fatalf("opt-in off should return nil, got %+v", got)
	}
}

func TestDoctorLSPDiagnosticsIssuesMissingOrCorrupt(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		if got := doctorLSPDiagnosticsIssues(doctorLSPDiagnosticsTestApp(t.TempDir(), true)); got != nil {
			t.Fatalf("missing snapshot should return nil, got %+v", got)
		}
	})
	t.Run("corrupt file", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, doctorLSPDiagnosticsReportFile), []byte("{not-json"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := doctorLSPDiagnosticsIssues(doctorLSPDiagnosticsTestApp(dir, true)); got != nil {
			t.Fatalf("corrupt snapshot should return nil, got %+v", got)
		}
	})
	t.Run("empty storage dir", func(t *testing.T) {
		if got := doctorLSPDiagnosticsIssues(doctorLSPDiagnosticsTestApp("", true)); got != nil {
			t.Fatalf("empty storage dir should return nil, got %+v", got)
		}
	})
	t.Run("no reports", func(t *testing.T) {
		dir := t.TempDir()
		writeDoctorLSPDiagnosticsSnapshot(t, dir, doctorLSPDiagnosticsSnapshot{})
		if got := doctorLSPDiagnosticsIssues(doctorLSPDiagnosticsTestApp(dir, true)); got != nil {
			t.Fatalf("empty snapshot should return nil, got %+v", got)
		}
	})
}

func TestDoctorLSPDiagnosticsIssuesLimitAndRedaction(t *testing.T) {
	dir := t.TempDir()
	const secret = "sk-test-abcdef0123456789"
	diagnostics := make([]doctorLSPDiagnostic, 25)
	for i := range diagnostics {
		diagnostics[i] = doctorLSPDiagnostic{
			Severity: "error",
			Message:  "token=" + secret,
		}
	}
	writeDoctorLSPDiagnosticsSnapshot(t, dir, doctorLSPDiagnosticsSnapshot{
		Reports: []doctorLSPDiagnosticsReport{{URI: "file:///big.go", Diagnostics: diagnostics}},
	})

	issues := doctorLSPDiagnosticsIssues(doctorLSPDiagnosticsTestApp(dir, true))
	if len(issues) != doctorLSPDiagnosticsMaxIssues {
		t.Fatalf("issues = %d, want %d", len(issues), doctorLSPDiagnosticsMaxIssues)
	}
	for _, issue := range issues {
		if strings.Contains(issue.Message, secret) {
			t.Fatalf("secret leaked into issue: %s", issue.Message)
		}
		if !strings.Contains(issue.Message, "[redacted:api key]") {
			t.Fatalf("secret was not redacted: %s", issue.Message)
		}
	}
}

func TestDoctorLSPDiagnosticsDoNotBreakNineChecks(t *testing.T) {
	dir := t.TempDir()
	writeDoctorLSPDiagnosticsSnapshot(t, dir, doctorLSPDiagnosticsSnapshot{
		Reports: []doctorLSPDiagnosticsReport{{
			URI:         "file:///a.go",
			Diagnostics: []doctorLSPDiagnostic{{Severity: "error", Message: "boom"}},
		}},
	})
	env := map[string]string{doctorLSPDiagnosticsEnv: "1"}
	ioState, out, _ := newTestIO("", env)
	app := App{IO: ioState, StorageDir: dir}
	code := RunWith(app, []string{"doctor", "--json"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0; out = %s", code, out.String())
	}
	var report struct {
		Checks   []map[string]any `json:"checks"`
		Failures int              `json:"failures"`
		Issues   []map[string]any `json:"issues"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out.String())
	}
	if len(report.Checks) != 10 || report.Failures != 0 {
		t.Fatalf("checks = %d, failures = %d, want 10/0", len(report.Checks), report.Failures)
	}
	if len(report.Issues) == 0 {
		t.Fatalf("expected LSP diagnostics issue in JSON output: %s", out.String())
	}
	seen := false
	for _, issue := range report.Issues {
		if issue["name"] == doctorLSPDiagnosticsIssueName && issue["level"] == statusWarn {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("LSP diagnostics issue missing from JSON output: %s", out.String())
	}
}
