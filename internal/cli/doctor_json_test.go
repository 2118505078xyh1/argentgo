package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// doctorJSONRaw 用宽松的结构接收 doctor --json 的输出，便于核对每条记录的
// 字段集合（尤其是 environment 中绝不能出现 Value 字段）。
type doctorJSONRaw struct {
	Checks      []map[string]any `json:"checks"`
	Failures    int              `json:"failures"`
	Environment []map[string]any `json:"environment"`
}

func TestDoctorJSONReportStructure(t *testing.T) {
	// ftp scheme 在 http.Client 层就直接报错，避免测试依赖真实网络或随机端口。
	ioState, out, errOut := newTestIO("", nil)
	app := App{IO: ioState}
	code := RunWith(app, []string{"doctor", "--json", "--base-url", "ftp://doctor.invalid"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr = %q; out = %s", code, errOut.String(), out.String())
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", errOut.String())
	}

	var report doctorJSONRaw
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("--json 输出不是合法 JSON: %v\n%s", err, out.String())
	}

	wantNames := []string{"config", "mcp", "storage", "provider", "web_search", "verify", "git", "go"}
	if len(report.Checks) != len(wantNames) {
		t.Fatalf("checks 数量 = %d, want %d: %s", len(report.Checks), len(wantNames), out.String())
	}
	seen := make(map[string]bool, len(wantNames))
	failures := 0
	for _, check := range report.Checks {
		for _, key := range []string{"name", "status", "detail"} {
			if _, ok := check[key]; !ok {
				t.Fatalf("check 缺少字段 %q: %v", key, check)
			}
		}
		if len(check) != 3 {
			t.Fatalf("check 字段数 = %d, want 3: %v", len(check), check)
		}
		name, _ := check["name"].(string)
		status, _ := check["status"].(string)
		seen[name] = true
		switch status {
		case statusOK, statusWarn, statusFail:
		default:
			t.Fatalf("check %q 非法 status %q", name, status)
		}
		if status == statusFail {
			failures++
		}
	}
	for _, name := range wantNames {
		if !seen[name] {
			t.Fatalf("checks 缺少 %q: %s", name, out.String())
		}
	}
	if report.Failures != failures {
		t.Fatalf("failures = %d, want %d", report.Failures, failures)
	}

	if len(report.Environment) == 0 {
		t.Fatalf("environment 为空: %s", out.String())
	}
	for _, probe := range report.Environment {
		for _, key := range []string{"name", "ok", "detail"} {
			if _, ok := probe[key]; !ok {
				t.Fatalf("environment probe 缺少字段 %q: %v", key, probe)
			}
		}
		// Value 保存原始 env 值与路径，绝不能出现在 JSON 中。
		if _, ok := probe["value"]; ok {
			t.Fatalf("environment probe 意外包含 Value: %v", probe)
		}
		if len(probe) != 3 {
			t.Fatalf("environment probe 字段数 = %d, want 3: %v", len(probe), probe)
		}
	}
}

func TestDoctorJSONFailuresAndExitCode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	// mcpServers 结构错误 → mcp 检查失败。
	if err := os.WriteFile(path, []byte(`{"mcpServers":"not-an-object"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ioState, out, _ := newTestIO("", nil)
	app := App{IO: ioState, ConfigPath: path, StorageDir: dir}
	code := RunWith(app, []string{"doctor", "--json", "--base-url", "ftp://doctor.invalid"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1; out = %s", code, out.String())
	}
	var report doctorJSONRaw
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("输出不是合法 JSON: %v", err)
	}
	if report.Failures != 1 {
		t.Fatalf("failures = %d, want 1: %s", report.Failures, out.String())
	}
	var mcpStatus string
	for _, check := range report.Checks {
		if check["name"] == "mcp" {
			mcpStatus, _ = check["status"].(string)
		}
	}
	if mcpStatus != statusFail {
		t.Fatalf("mcp status = %q, want fail", mcpStatus)
	}
}

func TestDoctorJSONRedactsDetail(t *testing.T) {
	checks := []doctorCheck{
		{name: "provider", status: statusWarn,
			detail: `https://alice:hunter2@api.example.test/v1 unreachable`},
		{name: "config", status: statusFail,
			detail: "request failed with sk-proj-secret123abc"},
	}
	ioState, out, _ := newTestIO("", nil)
	code := runDoctorJSON(App{IO: ioState}, context.Background(), doctorOptions{}, checks...)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	raw := out.String()
	for _, secret := range []string{"hunter2", "alice:", "secret123abc"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("输出泄漏 %q: %s", secret, raw)
		}
	}
	if !strings.Contains(raw, "[redacted:api key]") {
		t.Fatalf("缺少脱敏占位符: %s", raw)
	}
	if !strings.Contains(raw, "api.example.test") {
		t.Fatalf("host should be preserved for diagnostics: %s", raw)
	}
}

func TestDoctorJSONOmitsEnvValuesAndPaths(t *testing.T) {
	// 注入一个含有伪 key / 伪值的环境；JSON 中既不应出现这些值，也不应
	// 出现 shell/git 的原始路径（environment probe 的 Value 字段已剔除）。
	env := map[string]string{
		"SHELL":                "/opt/fake-shell/bash",
		"TERM":                 "fake-term-value",
		"ARGENT_FAKE_SECRET_1": "sk-fake-123456",
		"PATH":                 "/fake/bin",
	}
	ioState, out, _ := newTestIO("", env)
	code := runDoctorJSON(App{IO: ioState}, context.Background(), doctorOptions{},
		doctorCheck{name: "go", status: statusOK, detail: runtime.Version()})
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	raw := out.String()
	for _, leak := range []string{"/opt/fake-shell/bash", "fake-term-value", "ARGENT_FAKE_SECRET_1", "sk-fake-123456", "/fake/bin"} {
		if strings.Contains(raw, leak) {
			t.Fatalf("输出泄漏 %q: %s", leak, raw)
		}
	}
}

func TestDoctorHelpMentionsJSON(t *testing.T) {
	ioState, out, errOut := newTestIO("", nil)
	code := RunWith(App{IO: ioState}, []string{"doctor", "--json", "--help"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", errOut.String())
	}
	usage := out.String()
	if !strings.Contains(usage, "--json") {
		t.Fatalf("usage 未包含 --json: %s", usage)
	}
	if !strings.Contains(usage, "usage: argent doctor") {
		t.Fatalf("usage 被破坏: %s", usage)
	}
}

// TestDoctorTextOutputGoldenUnchanged 锁定 --json 分支引入之前 doctor 的
// 文本输出。所有可变输入都被固定：临时目录、缺失的 PATH（使 git 检测失败）、
// 受控 env、以及不依赖真实网络的 ftp URL（scheme 不被代理，错误文案固定）。
func TestDoctorTextOutputGoldenUnchanged(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json") // 不存在 → "(defaults)"
	// exec.LookPath 读取真实进程 PATH，而非注入的 env，因此这里显式固定。
	t.Setenv("PATH", filepath.Join(dir, "missing-bin"))
	env := map[string]string{
		"SHELL": "/bin/bash",
		"TERM":  "xterm-256color",
	}
	ioState, out, _ := newTestIO("", env)
	app := App{IO: ioState, StorageDir: dir, ConfigPath: configPath}
	code := RunWith(app, []string{"doctor", "--base-url", "ftp://doctor.invalid"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0; out = %s", code, out.String())
	}

	var golden strings.Builder
	fmt.Fprintf(&golden, "%-4s %-9s %s\n", statusOK, "config", configPath+" (defaults)")
	fmt.Fprintf(&golden, "%-4s %-9s %s\n", statusOK, "mcp", "no servers configured")
	fmt.Fprintf(&golden, "%-4s %-9s %s\n", statusOK, "storage", dir+" writable")
	fmt.Fprintf(&golden, "%-4s %-9s %s\n", statusWarn, "provider",
		`ftp://doctor.invalid unreachable: Get "ftp://doctor.invalid": unsupported protocol scheme "ftp"`)
	fmt.Fprintf(&golden, "%-4s %-9s %s\n", statusWarn, "web_search",
		"not configured (optional): set ARGENT_WEB_SEARCH_URL or BRAVE_API_KEY/TAVILY_API_KEY to enable web_search")
	fmt.Fprintf(&golden, "%-4s %-9s %s\n", statusWarn, "verify",
		"not configured (optional): set verifyCommand/verifyCommands in config, or enable auto_verify via ARGENT_OPT_INS or set ARGENT_EDIT_DIAGNOSTICS for post-edit verification")
	fmt.Fprintf(&golden, "%-4s %-9s %s\n", statusWarn, "git", "git not found on PATH")
	fmt.Fprintf(&golden, "%-4s %-9s %s\n", statusOK, "go", runtime.Version())

	fmt.Fprintln(&golden, "\nenvironment:")
	fmt.Fprintf(&golden, "  %-4s %-10s %s %s\n", statusOK, probeGoVersion, runtime.Version(), "Go runtime version")
	fmt.Fprintf(&golden, "  %-4s %-10s %s %s\n", statusOK, probePlatform, runtime.GOOS+"/"+runtime.GOARCH, "GOOS/GOARCH")
	fmt.Fprintf(&golden, "  %-4s %-10s %s %s\n", statusOK, probeShell, "bash", "shell detected")
	fmt.Fprintf(&golden, "  %-4s %-10s %s %s\n", statusOK, probeTerminal, "xterm-256color", "TERM/WT_SESSION detected")
	fmt.Fprintf(&golden, "  %-4s %-10s %s %s\n", statusWarn, probeGit, "", "git not found on PATH")
	fmt.Fprintf(&golden, "  %-4s %-10s %s %s\n", statusWarn, probeEnv, "",
		"missing groups: PATH, HOME/USERPROFILE, TMPDIR/TEMP/TMP")
	fmt.Fprintln(&golden, "  advice:")
	fmt.Fprintln(&golden, "    - git: install Git and add it to PATH (https://git-scm.com)")
	fmt.Fprintln(&golden, "    - env: set the missing key variables (missing groups: PATH, HOME/USERPROFILE, TMPDIR/TEMP/TMP)")

	if got := out.String(); got != golden.String() {
		t.Fatalf("无 flag 输出与 golden 不一致\ngot:\n%s\nwant:\n%s", got, golden.String())
	}
}
