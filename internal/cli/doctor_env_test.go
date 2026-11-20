package cli

import (
	"errors"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// testEnvGetter builds an envGetter from a map so probes can be driven without
// touching the process environment.
func testEnvGetter(values map[string]string) envGetter {
	return func(key string) string { return values[key] }
}

func testLookPath(found map[string]string) lookPathFunc {
	return func(name string) (string, error) {
		if path, ok := found[name]; ok {
			return path, nil
		}
		return "", errors.New("executable not found")
	}
}

func indexProbes(probes []ProbeResult) map[string]ProbeResult {
	byName := make(map[string]ProbeResult, len(probes))
	for _, p := range probes {
		byName[p.Name] = p
	}
	return byName
}

func TestProbeEnvironmentCompleteAndOrdered(t *testing.T) {
	env := testEnvGetter(map[string]string{
		"SHELL": "/bin/bash", "TERM": "xterm", "PATH": "/bin",
		"HOME": "/home/u", "TMPDIR": "/tmp",
	})
	probes := probeEnvironment(env, testLookPath(map[string]string{"git": "/usr/bin/git"}))

	wantNames := []string{probeGoVersion, probePlatform, probeShell, probeTerminal, probeGit, probeEnv}
	if len(probes) != len(wantNames) {
		t.Fatalf("probe count = %d, want %d", len(probes), len(wantNames))
	}
	for i, want := range wantNames {
		if probes[i].Name != want {
			t.Fatalf("probe[%d].Name = %q, want %q", i, probes[i].Name, want)
		}
	}
	for _, p := range probes {
		if p.Detail == "" {
			t.Errorf("probe %q has empty Detail", p.Name)
		}
	}
}

func TestProbeEnvironmentReportsRuntimeFacts(t *testing.T) {
	probes := indexProbes(probeEnvironment(testEnvGetter(nil), testLookPath(nil)))

	goVer := probes[probeGoVersion]
	if !goVer.OK || goVer.Value != runtime.Version() {
		t.Fatalf("go_version = %+v, want OK and %q", goVer, runtime.Version())
	}
	platform := probes[probePlatform]
	wantPlatform := runtime.GOOS + "/" + runtime.GOARCH
	if !platform.OK || platform.Value != wantPlatform {
		t.Fatalf("platform = %+v, want OK and %q", platform, wantPlatform)
	}
}

func TestProbeEnvironmentInjectedValues(t *testing.T) {
	env := testEnvGetter(map[string]string{
		"SHELL":       "/usr/bin/zsh",
		"TERM":        "xterm-256color",
		"WT_SESSION":  "",
		"PATH":        "/usr/bin",
		"HOME":        "/home/tester",
		"TMPDIR":      "/tmp",
		"USERPROFILE": "",
		"TEMP":        "",
		"TMP":         "",
	})
	probes := indexProbes(probeEnvironment(env, testLookPath(map[string]string{"git": "/usr/bin/git"})))

	if got := probes[probeShell]; !got.OK || got.Value != "zsh" {
		t.Errorf("shell = %+v, want OK and zsh", got)
	}
	if got := probes[probeTerminal]; !got.OK || got.Value != "xterm-256color" {
		t.Errorf("terminal = %+v, want OK and xterm-256color", got)
	}
	if got := probes[probeGit]; !got.OK || got.Value != "/usr/bin/git" {
		t.Errorf("git = %+v, want OK and /usr/bin/git", got)
	}
	envProbe := probes[probeEnv]
	if !envProbe.OK {
		t.Errorf("env probe should be healthy, got %+v", envProbe)
	}
	if want := "PATH,HOME,TMPDIR"; envProbe.Value != want {
		t.Errorf("env.Value = %q, want %q", envProbe.Value, want)
	}
}

func TestProbeEnvironmentWindowsTerminalAndCOMSPEC(t *testing.T) {
	env := testEnvGetter(map[string]string{
		"COMSPEC":     `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`,
		"WT_SESSION":  "session-guid",
		"USERPROFILE": `C:\Users\tester`,
		"PATH":        `C:\Windows`,
		"TEMP":        `C:\Temp`,
	})
	probes := indexProbes(probeEnvironment(env, testLookPath(map[string]string{"git": `C:\Program Files\Git\cmd\git.exe`})))

	if got := probes[probeShell]; !got.OK || got.Value != "powershell.exe" {
		t.Errorf("shell = %+v, want OK and powershell.exe", got)
	}
	if got := probes[probeTerminal]; !got.OK || got.Value != "session-guid" {
		t.Errorf("terminal = %+v, want OK and session-guid", got)
	}
	if got := probes[probeEnv]; !got.OK {
		t.Errorf("env = %+v, want OK via USERPROFILE/TEMP", got)
	}
}

func TestProbeEnvironmentMissingSignals(t *testing.T) {
	// With no shell/terminal, no key vars and no git, the three abnormal probes
	// must report OK=false with actionable details.
	probes := indexProbes(probeEnvironment(testEnvGetter(nil), testLookPath(nil)))

	for _, name := range []string{probeShell, probeTerminal, probeGit, probeEnv} {
		if probes[name].OK {
			t.Errorf("%s should be unhealthy with empty environment, got %+v", name, probes[name])
		}
	}
	if got := probes[probeEnv].Detail; !strings.Contains(got, "missing groups:") {
		t.Errorf("env detail = %q, want missing groups", got)
	}
	if got := probes[probeGit].Detail; got != "git not found on PATH" {
		t.Errorf("git detail = %q", got)
	}
}

func TestProbeEnvironmentCJKValuesSurvive(t *testing.T) {
	const cjkShell = "中文工具/Shell终端"
	const cjkTerm = "终端类型-中文"
	env := testEnvGetter(map[string]string{
		"SHELL":  cjkShell,
		"TERM":   cjkTerm,
		"PATH":   "路径",
		"HOME":   "主目录",
		"TMPDIR": "临时",
	})
	probes := indexProbes(probeEnvironment(env, testLookPath(map[string]string{"git": "中文/git路径"})))

	// shellName strips the last path component but must leave CJK runes intact.
	if got, want := probes[probeShell].Value, "Shell终端"; got != want {
		t.Errorf("CJK shell = %q, want %q", got, want)
	}
	if got := probes[probeTerminal].Value; got != cjkTerm {
		t.Errorf("CJK terminal = %q, want %q", got, cjkTerm)
	}
	if got := probes[probeGit].Value; got != "中文/git路径" {
		t.Errorf("CJK git path = %q", got)
	}
	if got := probes[probeEnv].Value; got != "PATH,HOME,TMPDIR" {
		t.Errorf("env.Value = %q, want PATH,HOME,TMPDIR", got)
	}
}

func TestShellNameSplitsBothSeparators(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"bash", "bash"},
		{"/bin/bash", "bash"},
		{`C:\Windows\cmd.exe`, "cmd.exe"},
		{`C:/Program Files/Git/git.exe`, "git.exe"},
		{"中文/Shell终端", "Shell终端"},
	}
	for _, tt := range tests {
		if got := shellName(tt.in); got != tt.want {
			t.Errorf("shellName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestDoctorAdviceForAbnormalProbes(t *testing.T) {
	all := probeEnvironment(testEnvGetter(nil), testLookPath(nil))
	advice := doctorAdvice(all)

	if len(advice) != 4 {
		t.Fatalf("advice count = %d, want 4 (shell, terminal, git, env); got %q", len(advice), advice)
	}
	wantFragments := []string{
		"shell:",
		"terminal:",
		"git: install Git",
		"env: set the missing key variables",
	}
	for i, fragment := range wantFragments {
		if !strings.Contains(advice[i], fragment) {
			t.Errorf("advice[%d] = %q, want fragment %q", i, advice[i], fragment)
		}
	}
}

func TestDoctorAdviceIgnoresHealthyProbes(t *testing.T) {
	probes := []ProbeResult{
		{Name: probeGoVersion, OK: true},
		{Name: probePlatform, OK: true},
		{Name: probeShell, OK: true},
	}
	if got := doctorAdvice(probes); len(got) != 0 {
		t.Fatalf("healthy probes produced advice: %q", got)
	}
}

func TestDoctorAdviceFallsBackForUnknownProbe(t *testing.T) {
	got := doctorAdvice([]ProbeResult{{Name: "custom", OK: false}})
	if len(got) != 1 || !strings.Contains(got[0], "custom") {
		t.Fatalf("fallback advice = %q", got)
	}
}

func TestProbeEnvironmentAndAdviceAreStable(t *testing.T) {
	env := testEnvGetter(map[string]string{"SHELL": "/bin/sh", "TERM": "screen"})
	lookPath := testLookPath(map[string]string{"git": "/usr/bin/git"})

	first := probeEnvironment(env, lookPath)
	second := probeEnvironment(env, lookPath)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("probe output not stable:\nfirst:  %+v\nsecond: %+v", first, second)
	}
	if !reflect.DeepEqual(doctorAdvice(first), doctorAdvice(second)) {
		t.Fatal("advice output not stable")
	}
}

func TestWriteDoctorEnvironmentOutputIsStableAndCJKSafe(t *testing.T) {
	probes := []ProbeResult{
		{Name: probeGoVersion, Value: "go1.27.4", OK: true, Detail: "Go runtime version"},
		{Name: probeShell, Value: "中文终端", OK: false, Detail: "SHELL/COMSPEC not set"},
	}
	advice := doctorAdvice(probes)

	var first, second strings.Builder
	writeDoctorEnvironment(&first, probes, advice)
	writeDoctorEnvironment(&second, probes, advice)
	if first.String() != second.String() {
		t.Fatalf("environment output not stable:\n%q\n%q", first.String(), second.String())
	}

	got := first.String()
	for _, want := range []string{
		"\nenvironment:\n",
		"  ok   go_version",
		"go1.27.4 Go runtime version",
		"中文终端",
		"  advice:\n",
		"    - shell: set SHELL",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("environment output missing %q; got:\n%s", want, got)
		}
	}
}

func TestDoctorAppendsEnvironmentSection(t *testing.T) {
	// Doctor's pre-existing lines must stay intact and the environment section
	// must appear at the end, without affecting the exit code.
	env := testEnvGetter(map[string]string{
		"SHELL": "/bin/bash", "TERM": "xterm", "PATH": "/usr/bin",
		"HOME": "/home/u", "TMPDIR": "/tmp",
	})
	ioState, out, _ := newTestIO("", nil)
	ioState.Env = env
	app := App{IO: ioState, StorageDir: t.TempDir()}

	// Point the provider at a disabled loopback port: it becomes a harmless
	// warning so the run still exits 0.
	code := RunWith(app, []string{"doctor", "--base-url", "http://127.0.0.1:1"})
	if code != 0 {
		t.Fatalf("exit = %d, out:\n%s", code, out.String())
	}
	got := out.String()
	for _, want := range []string{"config", "mcp", "storage", "provider", "git", "go", "\nenvironment:\n", "go_version", "platform", "shell", "terminal", "git", "env"} {
		if !strings.Contains(got, want) {
			t.Errorf("doctor output missing %q; got:\n%s", want, got)
		}
	}
	idx := strings.Index(got, "\nenvironment:\n")
	if idx < 0 {
		t.Fatalf("environment section missing; got:\n%s", got)
	}
	tail := got[idx:]
	if !strings.Contains(tail, probeGoVersion) || !strings.Contains(tail, probeEnv) {
		t.Fatalf("environment section incomplete; tail:\n%s", tail)
	}
}
