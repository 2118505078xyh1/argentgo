package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/config"
)

// TestValidateVerifyArgvAllowlist pins the local copy of the eval exec allowlist:
// only `go test|build ...` and a relative .exe are accepted, and every known
// escape (shells, absolute paths, traversal, metacharacters, -exec/-toolexec)
// is refused.
func TestValidateVerifyArgvAllowlist(t *testing.T) {
	tests := []struct {
		name   string
		argv   []string
		wantOK bool
	}{
		{"go test", []string{"go", "test", "./..."}, true},
		{"go build", []string{"go", "build", "./..."}, true},
		{"relative exe backslash", []string{`tools\check.exe`}, true},
		{"relative exe slash with args", []string{"tools/check.exe", "--quiet"}, true},
		{"empty", nil, false},
		{"empty command", []string{"  "}, false},
		{"sh", []string{"sh", "-c", "echo hi"}, false},
		{"bash", []string{"bash", "script.sh"}, false},
		{"curl", []string{"curl", "http://example.com/x"}, false},
		{"absolute windows", []string{`C:\Windows\System32\cmd.exe`}, false},
		{"absolute unix", []string{"/usr/bin/go", "test", "./..."}, false},
		{"traversal", []string{"../evil.exe"}, false},
		{"nested traversal", []string{"bin/../evil.exe"}, false},
		{"metachar semicolon", []string{"go", "test", "./...;echo hi"}, false},
		{"metachar pipe", []string{"go", "test", "./...|cat"}, false},
		{"metachar substitution", []string{"go", "test", "$(whoami)"}, false},
		{"exec flag", []string{"go", "test", "-exec", "evil"}, false},
		{"toolexec flag", []string{"go", "build", "-toolexec", "evil"}, false},
		{"toolexec equals", []string{"go", "test", "-toolexec=evil"}, false},
		{"go run", []string{"go", "run", "."}, false},
		{"go without subcommand", []string{"go"}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateVerifyArgv(tc.argv)
			if tc.wantOK && err != nil {
				t.Fatalf("validateVerifyArgv(%q) = %v, want allowed", tc.argv, err)
			}
			if !tc.wantOK && err == nil {
				t.Fatalf("validateVerifyArgv(%q) = nil, want rejected", tc.argv)
			}
		})
	}
}

// TestNewVerifyFuncMisconfigurationIsUnrunnable checks that an empty or
// disallowed command yields ok=false, so the loop injects no feedback.
func TestNewVerifyFuncMisconfigurationIsUnrunnable(t *testing.T) {
	for _, argv := range [][]string{nil, {"bash", "-c", "echo hi"}} {
		verify := NewVerifyFunc(argv, 0, t.TempDir())
		label, passed, output, ok := verify(context.Background(), []string{"main.go"})
		if ok {
			t.Errorf("argv %q: ok = true, want false", argv)
		}
		if passed {
			t.Errorf("argv %q: passed = true, want false", argv)
		}
		if output == "" {
			t.Errorf("argv %q: empty rejection reason", argv)
		}
		if len(argv) > 0 && label == "" {
			t.Errorf("argv %q: empty label", argv)
		}
	}
}

func TestVerifyEnvironmentStripsGOFLAGS(t *testing.T) {
	t.Setenv("GOFLAGS", "-toolexec=evil")
	for _, entry := range verifyEnvironment() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(key, "GOFLAGS") {
			t.Fatalf("verifyEnvironment leaked %q", entry)
		}
	}
}

// verifyProbeProgram is a trivial temp Go module used to exercise the runner
// without a shell. Modes: default exit 0, "fail" exits 1 with a planted secret,
// "huge" writes 200KB, and "sleep" blocks until killed by the deadline.
const verifyProbeProgram = `package main

import (
	"fmt"
	"os"
	"strings"
	"time"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "fail":
			fmt.Println("BOOM sk-abcdefghijklmnop1234")
			os.Exit(1)
		case "huge":
			fmt.Println(strings.Repeat("x", 200000))
			return
		case "sleep":
			time.Sleep(30 * time.Second)
			return
		}
	}
	fmt.Println("PASS")
}
`

// buildVerifyProbe builds the probe inside workspace and returns its relative
// .exe name. It skips when the Go toolchain is unavailable.
func buildVerifyProbe(t *testing.T, workspace string) string {
	t.Helper()
	writeVerifyFile(t, workspace, "go.mod", "module verifyprobe\n\ngo 1.21\n")
	writeVerifyFile(t, workspace, "main.go", verifyProbeProgram)

	const exe = "verifyprobe.exe"
	cmd := exec.Command("go", "build", "-o", exe, ".")
	cmd.Dir = workspace
	cmd.Env = verifyEnvironment()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("Go toolchain unavailable for probe build: %v: %s", err, out)
	}
	return exe
}

func writeVerifyFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// TestNewVerifyFuncOutcomes drives pass, fail (with redaction), bounded output
// and timeout through a relative .exe built from a trivial temp Go module.
func TestNewVerifyFuncOutcomes(t *testing.T) {
	workspace := t.TempDir()
	exe := buildVerifyProbe(t, workspace)

	t.Run("pass", func(t *testing.T) {
		verify := NewVerifyFunc([]string{exe}, 5000, workspace)
		label, passed, output, ok := verify(context.Background(), nil)
		if !ok || !passed {
			t.Fatalf("got (label=%q, passed=%v, output=%q, ok=%v), want a passing verdict", label, passed, output, ok)
		}
		if label != exe || !strings.Contains(output, "PASS") {
			t.Fatalf("label = %q, output = %q", label, output)
		}
	})

	t.Run("fail is redacted", func(t *testing.T) {
		verify := NewVerifyFunc([]string{exe, "fail"}, 5000, workspace)
		_, passed, output, ok := verify(context.Background(), nil)
		if !ok || passed {
			t.Fatalf("got (passed=%v, output=%q, ok=%v), want a failing verdict", passed, output, ok)
		}
		if !strings.Contains(output, "BOOM") {
			t.Fatalf("output lost failure context: %q", output)
		}
		if strings.Contains(output, "sk-abcdefghijklmnop1234") {
			t.Fatalf("output leaked planted secret: %q", output)
		}
	})

	t.Run("output is bounded", func(t *testing.T) {
		verify := NewVerifyFunc([]string{exe, "huge"}, 5000, workspace)
		_, passed, output, ok := verify(context.Background(), nil)
		if !ok || !passed {
			t.Fatalf("got (passed=%v, ok=%v), want a passing verdict", passed, ok)
		}
		if len(output) > verifyOutputCap+len(verifyTruncMarker) {
			t.Fatalf("output length = %d, want <= %d", len(output), verifyOutputCap+len(verifyTruncMarker))
		}
		if !strings.Contains(output, verifyTruncMarker) {
			t.Fatalf("large output was not marked as truncated")
		}
	})

	t.Run("timeout", func(t *testing.T) {
		verify := NewVerifyFunc([]string{exe, "sleep"}, 150, workspace)
		start := time.Now()
		_, passed, output, ok := verify(context.Background(), nil)
		if !ok || passed {
			t.Fatalf("got (passed=%v, output=%q, ok=%v), want a timed-out failure", passed, output, ok)
		}
		if !strings.Contains(output, "timed out") {
			t.Fatalf("timeout detail missing: %q", output)
		}
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Fatalf("timeout took %s, want a bounded kill", elapsed)
		}
	})
}

// TestNewVerifyFuncAcceptsGoTest exercises the `go` branch with a real (tiny)
// temp module. It skips when go is not on PATH.
func TestNewVerifyFuncAcceptsGoTest(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go toolchain not on PATH: %v", err)
	}
	workspace := t.TempDir()
	writeVerifyFile(t, workspace, "go.mod", "module verifygo\n\ngo 1.21\n")
	writeVerifyFile(t, workspace, "main_test.go", "package verifygo\n\nimport \"testing\"\n\nfunc TestOK(t *testing.T) {}\n")

	verify := NewVerifyFunc([]string{"go", "test", "./..."}, 120000, workspace)
	_, passed, output, ok := verify(context.Background(), nil)
	if !ok || !passed {
		t.Fatalf("go test ./... = (passed=%v, ok=%v, output=%q), want a passing verdict", passed, ok, output)
	}
}

func verifyTestEnv(home string) func(string) string {
	return func(key string) string {
		switch key {
		case "HOME", "USERPROFILE":
			return home
		}
		return ""
	}
}

// TestConfigDecodeVerifySettings pins the config -> Config field decode for
// both the file layer and the override layer (which is how the CLI flags land).
func TestConfigDecodeVerifySettings(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	writeVerifyFile(t, root, "argent.json",
		`{"verifyCommand":["go","test","./..."],"verifyTimeoutMS":1234,"verifyMaxReflections":5}`)

	cfg, err := config.Load(root, verifyTestEnv(home))
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if got := strings.Join(cfg.VerifyCommand, " "); got != "go test ./..." {
		t.Fatalf("VerifyCommand = %v", cfg.VerifyCommand)
	}
	if cfg.VerifyTimeoutMS != 1234 {
		t.Fatalf("VerifyTimeoutMS = %d, want 1234", cfg.VerifyTimeoutMS)
	}
	if cfg.VerifyMaxReflections != 5 {
		t.Fatalf("VerifyMaxReflections = %d, want 5", cfg.VerifyMaxReflections)
	}

	// CLI flags write typed overrides; the same shape must win over the file.
	cfg, err = config.LoadWithOverrides(root, verifyTestEnv(home), map[string]any{
		"verifyCommand":   []any{"go", "build", "./..."},
		"verifyTimeoutMS": 4321,
	})
	if err != nil {
		t.Fatalf("config.LoadWithOverrides: %v", err)
	}
	if got := strings.Join(cfg.VerifyCommand, " "); got != "go build ./..." {
		t.Fatalf("override VerifyCommand = %v", cfg.VerifyCommand)
	}
	if cfg.VerifyTimeoutMS != 4321 {
		t.Fatalf("override VerifyTimeoutMS = %d, want 4321", cfg.VerifyTimeoutMS)
	}
}

func TestConfigVerifyDefaultsAreOptIn(t *testing.T) {
	cfg, err := config.Load(t.TempDir(), verifyTestEnv(t.TempDir()))
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if len(cfg.VerifyCommand) != 0 {
		t.Fatalf("VerifyCommand = %v, want empty (feature off)", cfg.VerifyCommand)
	}
	if cfg.VerifyTimeoutMS != 30000 {
		t.Fatalf("VerifyTimeoutMS = %d, want 30000", cfg.VerifyTimeoutMS)
	}
}

// TestVerifyWiringDefaultOffAndConfigured pins the helpers used by the Options
// construction in app_execute.go: no command means nil Verify and a zero cap
// (byte-identical Options); a configured command enables the loop with a cap.
func TestVerifyWiringDefaultOffAndConfigured(t *testing.T) {
	if got := verifyFuncFor(Resolved{}); got != nil {
		t.Fatal("verifyFuncFor(empty) = non-nil, want nil")
	}
	if got := verifyReflectionsFor(Resolved{}); got != 0 {
		t.Fatalf("verifyReflectionsFor(empty) = %d, want 0", got)
	}

	res := Resolved{
		Cfg:  config.Config{VerifyCommand: []string{"go", "test", "./..."}},
		Root: t.TempDir(),
	}
	if got := verifyFuncFor(res); got == nil {
		t.Fatal("verifyFuncFor(configured) = nil, want callback")
	}
	if got := verifyReflectionsFor(res); got != agent.DefaultMaxVerifyReflections {
		t.Fatalf("verifyReflectionsFor(configured) = %d, want %d", got, agent.DefaultMaxVerifyReflections)
	}

	res.Cfg.VerifyMaxReflections = 7
	if got := verifyReflectionsFor(res); got != 7 {
		t.Fatalf("verifyReflectionsFor(explicit) = %d, want 7", got)
	}
}

// joinedArgvs renders a selection result for table comparisons.
func joinedArgvs(argvs [][]string) []string {
	out := make([]string, len(argvs))
	for i, argv := range argvs {
		out[i] = strings.Join(argv, " ")
	}
	return out
}

func assertArgvs(t *testing.T, got [][]string, want ...string) {
	t.Helper()
	g := joinedArgvs(got)
	if strings.Join(g, "|") != strings.Join(want, "|") {
		t.Fatalf("selected = %q, want %q", g, want)
	}
}

// TestSelectVerifyCommandsByExtension pins per-language dispatch: dotted and
// bare keys, aliases and case differences all map a file extension to the
// configured command without spawning a process.
func TestSelectVerifyCommandsByExtension(t *testing.T) {
	commands := map[string]string{
		".go":        "go test ./go/...",
		"py":         "go test ./py/...",
		"TypeScript": "go test ./ts/...",
	}
	tests := []struct {
		name string
		path string
		want string
	}{
		{"dotted key", "internal/app/main.go", "go test ./go/..."},
		{"bare key", "internal/app/main.GO", "go test ./go/..."},
		{"alias py", "scripts/tool.py", "go test ./py/..."},
		{"python alias", "scripts/tool.python", "go test ./py/..."},
		{"case insensitive key", "web/app.tsx", "go test ./ts/..."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertArgvs(t, selectVerifyArgvs(commands, nil, []string{tc.path}), tc.want)
		})
	}
}

// TestSelectVerifyCommandsUnknownExtensionFallback pins the fallback: a path
// with no matching language uses the single VerifyCommand; with no fallback at
// all, nothing is selected so the loop skips verification for that batch.
func TestSelectVerifyCommandsUnknownExtensionFallback(t *testing.T) {
	commands := map[string]string{".go": "go test ./go/..."}
	fallback := []string{"go", "build", "./..."}

	assertArgvs(t, selectVerifyArgvs(commands, fallback, []string{"docs/readme.txt"}),
		"go build ./...")
	assertArgvs(t, selectVerifyArgvs(commands, fallback, []string{"main.go", "docs/readme.txt"}),
		"go test ./go/...", "go build ./...")
	assertArgvs(t, selectVerifyArgvs(commands, nil, []string{"docs/readme.txt"}))
	// No checkpoint paths still exercises the fallback once.
	assertArgvs(t, selectVerifyArgvs(commands, fallback, nil), "go build ./...")
}

// TestSelectVerifyCommandsMultipleFiles pins multi-file dispatch: every
// distinct language command is selected once, in first-seen order, regardless
// of how many files share that language.
func TestSelectVerifyCommandsMultipleFiles(t *testing.T) {
	commands := map[string]string{
		".go": "go test ./go/...",
		".py": "go test ./py/...",
		".rs": "go test ./rs/...",
	}
	paths := []string{"a.go", "b.go", "x.py", "c.go", "d.rs", "y.py"}
	assertArgvs(t, selectVerifyArgvs(commands, nil, paths),
		"go test ./go/...", "go test ./py/...", "go test ./rs/...")
}

// TestVerifyWiringPerLanguageDispatch pins that the per-language map enables
// the loop on its own (without a single command), keeps reflections wired, and
// that a configured single command alone still takes the legacy path.
func TestVerifyWiringPerLanguageDispatch(t *testing.T) {
	root := t.TempDir()
	res := Resolved{
		Cfg:  config.Config{VerifyCommands: map[string]string{".go": "go test ./..."}},
		Root: root,
	}
	if got := verifyFuncFor(res); got == nil {
		t.Fatal("verifyFuncFor(map only) = nil, want dispatcher")
	}
	if got := verifyReflectionsFor(res); got != agent.DefaultMaxVerifyReflections {
		t.Fatalf("verifyReflectionsFor(map only) = %d, want %d", got, agent.DefaultMaxVerifyReflections)
	}
	// A rejected command string yields ok=false but must not crash dispatch.
	res.Cfg.VerifyCommands = map[string]string{".go": "bash -c echo hi"}
	_, _, _, ok := verifyFuncFor(res)(context.Background(), []string{"main.go"})
	if ok {
		t.Fatal("dispatcher with disallowed command returned ok=true, want false")
	}
}

// TestConfigDecodeVerifyCommands pins the config -> Config decode for the new
// map at both the file and override layers, alongside the legacy single argv.
func TestConfigDecodeVerifyCommands(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	writeVerifyFile(t, root, "argent.json", `{
		"verifyCommand": ["go", "test", "./..."],
		"verifyCommands": {".go": "go test ./go/...", "py": "go test ./py/..."}
	}`)

	cfg, err := config.Load(root, verifyTestEnv(home))
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if got := cfg.VerifyCommands[".go"]; got != "go test ./go/..." {
		t.Fatalf("VerifyCommands[.go] = %q", got)
	}
	if got := cfg.VerifyCommands["py"]; got != "go test ./py/..." {
		t.Fatalf("VerifyCommands[py] = %q", got)
	}
	if len(cfg.VerifyCommand) == 0 {
		t.Fatal("VerifyCommand = empty, want legacy argv preserved")
	}

	cfg, err = config.LoadWithOverrides(root, verifyTestEnv(home), map[string]any{
		"verifyCommands": map[string]any{".go": "go build ./go/..."},
	})
	if err != nil {
		t.Fatalf("config.LoadWithOverrides: %v", err)
	}
	if got := cfg.VerifyCommands[".go"]; got != "go build ./go/..." {
		t.Fatalf("override VerifyCommands[.go] = %q", got)
	}
}
