package eval

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedExecWorkspace writes a tiny module into a throwaway directory. It mirrors
// the harness' workspace seeding without depending on run.go.
func seedExecWorkspace(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("seed %q: %v", name, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("seed %q: %v", name, err)
		}
	}
	return dir
}

func TestExecCheckValidate(t *testing.T) {
	absTool := filepath.Join(os.TempDir(), "tool.exe")
	tests := []struct {
		name    string
		check   ExecCheck
		wantErr error
	}{
		{"go test allowed", ExecCheck{Command: "go", Args: []string{"test", "./..."}}, nil},
		{"go build allowed", ExecCheck{Command: "go", Args: []string{"build", "./..."}}, nil},
		{"relative exe allowed", ExecCheck{Command: "tools/run.exe", Args: []string{"--flag"}}, nil},
		{"root relative exe allowed", ExecCheck{Command: "run.exe"}, nil},
		{"go run denied", ExecCheck{Command: "go", Args: []string{"run", "main.go"}}, ErrExecGoSubcommand},
		{"go with no subcommand denied", ExecCheck{Command: "go"}, ErrExecGoSubcommand},
		{"absolute exe denied", ExecCheck{Command: absTool}, ErrExecBadPath},
		{"parent traversal denied", ExecCheck{Command: "../tools/run.exe"}, ErrExecBadPath},
		{"inner parent traversal denied", ExecCheck{Command: "tools/../run.exe"}, ErrExecBadPath},
		{"sh denied", ExecCheck{Command: "sh", Args: []string{"-c", "echo hi"}}, ErrExecCommandDenied},
		{"bash denied", ExecCheck{Command: "bash", Args: []string{"-c", "echo hi"}}, ErrExecCommandDenied},
		{"curl denied", ExecCheck{Command: "curl", Args: []string{"http://example.com"}}, ErrExecCommandDenied},
		{"unknown command denied", ExecCheck{Command: "python"}, ErrExecCommandDenied},
		{"pipe in argument denied", ExecCheck{Command: "go", Args: []string{"test", "./... | curl x"}}, ErrExecShellMeta},
		{"semicolon in argument denied", ExecCheck{Command: "go", Args: []string{"test", "./...; calc.exe"}}, ErrExecShellMeta},
		{"semicolon in command denied", ExecCheck{Command: "go;calc.exe"}, ErrExecShellMeta},
		{"dangerous exec flag denied", ExecCheck{Command: "go", Args: []string{"test", "-exec=evil.exe"}}, ErrExecDangerousGoFlag},
		{"dangerous toolexec flag denied", ExecCheck{Command: "go", Args: []string{"build", "-toolexec=evil.exe"}}, ErrExecDangerousGoFlag},
		{"empty command denied", ExecCheck{}, ErrExecEmptyCommand},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.check.Validate()
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Validate() = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestRunExecCheckRejectsDisallowedCommand(t *testing.T) {
	c := ExecCheck{Command: "curl", Args: []string{"http://example.com"}, TimeoutMS: 1000}
	res := RunExecCheck(t.TempDir(), c)
	if !errors.Is(res.Err, ErrExecCommandDenied) {
		t.Fatalf("Err = %v, want ErrExecCommandDenied", res.Err)
	}
	if res.ExitCode != -1 {
		t.Fatalf("ExitCode = %d, want -1", res.ExitCode)
	}
	if GradeExecCheck(res, c) {
		t.Fatal("a rejected command must never grade as a pass")
	}
}

func TestRunExecCheckGoTestPassesInSeededWorkspace(t *testing.T) {
	dir := seedExecWorkspace(t, map[string]string{
		"go.mod": "module example.com/execpass\n\ngo 1.27\n",
		"sum.go": `package execpass

func Sum(a, b int) int { return a + b }
`,
		"marker.txt": "seeded\n",
		"sum_test.go": `package execpass

import (
	"os"
	"testing"
)

func TestSumReadsSeededWorkspace(t *testing.T) {
	b, err := os.ReadFile("marker.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "seeded\n" {
		t.Fatalf("marker = %q", b)
	}
	if Sum(1, 2) != 3 {
		t.Fatal("bad sum")
	}
}
`,
	})

	c := ExecCheck{
		Command:            "go",
		Args:               []string{"test", "./..."},
		TimeoutMS:          20000,
		WantExitCode:       0,
		WantStdoutContains: "ok",
	}
	res := RunExecCheck(dir, c)
	if res.TimedOut {
		t.Fatalf("unexpected timeout: err=%v", res.Err)
	}
	if res.Err != nil || res.ExitCode != 0 {
		t.Fatalf("go test failed: exit=%d err=%v stderr=%q", res.ExitCode, res.Err, res.StderrTail)
	}
	if !strings.Contains(res.StdoutTail, "ok") {
		t.Fatalf("stdout tail = %q, want it to contain ok", res.StdoutTail)
	}
	if !GradeExecCheck(res, c) {
		t.Fatal("GradeExecCheck = false, want true")
	}
}

func TestRunExecCheckFailingGoTestGradesExpectedNonZero(t *testing.T) {
	dir := seedExecWorkspace(t, map[string]string{
		"go.mod": "module example.com/execfail\n\ngo 1.27\n",
		"broken_test.go": `package execfail

import "testing"

func TestAlwaysFails(t *testing.T) {
	t.Fatal("intentional failure")
}
`,
	})

	c := ExecCheck{
		Command:      "go",
		Args:         []string{"test", "./..."},
		TimeoutMS:    20000,
		WantExitCode: 1,
	}
	res := RunExecCheck(dir, c)
	if res.TimedOut {
		t.Fatalf("unexpected timeout: err=%v", res.Err)
	}
	if res.ExitCode == 0 {
		t.Fatalf("expected a non-zero exit, got 0 (stdout=%q)", res.StdoutTail)
	}
	// A real *exec.ExitError is expected here; it must not block a grade that
	// explicitly wants this non-zero exit code.
	if !GradeExecCheck(res, c) {
		t.Fatalf("GradeExecCheck = false for expected exit %d; err=%v stderr=%q", res.ExitCode, res.Err, res.StderrTail)
	}

	wrong := c
	wrong.WantExitCode = 0
	if GradeExecCheck(res, wrong) {
		t.Fatal("GradeExecCheck = true for the wrong expected exit code")
	}
}

func TestRunExecCheckTimeoutIsAlwaysAFailure(t *testing.T) {
	dir := seedExecWorkspace(t, map[string]string{
		"go.mod": "module example.com/exectimeout\n\ngo 1.27\n",
	})

	c := ExecCheck{
		Command:      "go",
		Args:         []string{"test", "./..."},
		TimeoutMS:    1,
		WantExitCode: 0,
	}
	res := RunExecCheck(dir, c)
	if !res.TimedOut {
		t.Fatalf("TimedOut = false, exit=%d err=%v", res.ExitCode, res.Err)
	}
	if GradeExecCheck(res, c) {
		t.Fatal("a timed-out run must never grade as a pass")
	}
}

func TestRunExecCheckBoundsCapturedOutput(t *testing.T) {
	dir := seedExecWorkspace(t, map[string]string{
		"go.mod": "module example.com/execloud\n\ngo 1.27\n",
		"loud_test.go": `package execloud

import (
	"fmt"
	"strings"
	"testing"
)

func TestLoud(t *testing.T) {
	fmt.Println(strings.Repeat("x", 100000))
}
`,
	})

	c := ExecCheck{
		Command:      "go",
		Args:         []string{"test", "-v", "./..."},
		TimeoutMS:    20000,
		WantExitCode: 0,
	}
	res := RunExecCheck(dir, c)
	if res.TimedOut || res.ExitCode != 0 {
		t.Fatalf("go test did not finish cleanly: exit=%d timedOut=%t err=%v", res.ExitCode, res.TimedOut, res.Err)
	}
	if !strings.HasPrefix(res.StdoutTail, execTruncMarker) {
		t.Fatalf("stdout tail = %q, want the truncation marker prefix", clip(res.StdoutTail, 80))
	}
	if len(res.StdoutTail) > execOutputCap+len(execTruncMarker) {
		t.Fatalf("stdout tail has %d bytes, want at most %d", len(res.StdoutTail), execOutputCap+len(execTruncMarker))
	}
	if len(res.StderrTail) > execOutputCap+len(execTruncMarker) {
		t.Fatalf("stderr tail has %d bytes, want at most %d", len(res.StderrTail), execOutputCap+len(execTruncMarker))
	}
}

func TestGradeExecCheck(t *testing.T) {
	tests := []struct {
		name  string
		res   ExecResult
		check ExecCheck
		want  bool
	}{
		{"matching exit passes", ExecResult{ExitCode: 0}, ExecCheck{WantExitCode: 0}, true},
		{"wrong exit fails", ExecResult{ExitCode: 1}, ExecCheck{WantExitCode: 0}, false},
		{"expected non-zero passes", ExecResult{ExitCode: 2}, ExecCheck{WantExitCode: 2}, true},
		{"stdout needle present", ExecResult{ExitCode: 0, StdoutTail: "ok example.com/pkg"}, ExecCheck{WantExitCode: 0, WantStdoutContains: "ok"}, true},
		{"stdout needle absent", ExecResult{ExitCode: 0, StdoutTail: "FAIL"}, ExecCheck{WantExitCode: 0, WantStdoutContains: "ok"}, false},
		{"timeout never passes", ExecResult{ExitCode: 0, TimedOut: true}, ExecCheck{WantExitCode: 0}, false},
		{"start error never passes", ExecResult{ExitCode: -1, Err: errors.New("cannot start")}, ExecCheck{WantExitCode: -1}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GradeExecCheck(tt.res, tt.check); got != tt.want {
				t.Fatalf("GradeExecCheck() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestExecCheckFixtureLoadsAndValidates(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "evals", "exec_check.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var doc struct {
		Description string `json:"description"`
		Cases       []struct {
			ID             string            `json:"id"`
			Name           string            `json:"name"`
			WorkspaceFiles map[string]string `json:"workspace_files"`
			Check          ExecCheck         `json:"check"`
			Expect         struct {
				Rejected bool   `json:"rejected"`
				ExitCode *int   `json:"exit_code"`
				TimedOut bool   `json:"timed_out"`
				Grade    *bool  `json:"grade"`
				Note     string `json:"note"`
			} `json:"expect"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	if doc.Description == "" {
		t.Fatal("fixture is missing its description")
	}
	if len(doc.Cases) < 3 {
		t.Fatalf("fixture has %d cases, want at least 3", len(doc.Cases))
	}

	seen := make(map[string]bool, len(doc.Cases))
	for _, tc := range doc.Cases {
		t.Run(tc.ID, func(t *testing.T) {
			if tc.ID == "" || tc.Name == "" {
				t.Fatal("fixture case is missing id or name")
			}
			if seen[tc.ID] {
				t.Fatalf("duplicate case id %q", tc.ID)
			}
			seen[tc.ID] = true

			err := tc.Check.Validate()
			if tc.Expect.Rejected {
				if err == nil {
					t.Fatal("Validate() accepted a check the fixture marks rejected")
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
			if tc.Expect.ExitCode == nil {
				t.Fatal("fixture case is missing expect.exit_code")
			}
			if tc.Expect.Grade == nil {
				t.Fatal("fixture case is missing expect.grade")
			}
		})
	}
}
