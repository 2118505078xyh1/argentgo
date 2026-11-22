package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDoctorModelInfoIssuesFourFields 验证配置 provider/model 时产生一条稳定的
// warn 级 modelInfo issue，消息含 thinking/image/tools/contextWindow 四字段及
// provider.ProfileFor 推导出的真实值（anthropic + claude-3-opus 为全 true、200000）。
func TestDoctorModelInfoIssuesFourFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path,
		[]byte(`{"provider":"anthropic","model":"claude-3-opus"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ioState, _, _ := newTestIO("", nil)
	app := App{IO: ioState, ConfigPath: path}

	issues := doctorModelInfoIssues(app)
	if len(issues) != 1 {
		t.Fatalf("issues = %d, want 1: %+v", len(issues), issues)
	}
	issue := issues[0]
	if issue.Name != doctorModelInfoIssueName || issue.Level != statusWarn {
		t.Fatalf("issue = %+v, want warn/%s", issue, doctorModelInfoIssueName)
	}
	for _, field := range []string{
		"provider=anthropic", "model=claude-3-opus",
		"thinking=true", "image=true", "tools=true", "contextWindow=200000",
	} {
		if !strings.Contains(issue.Message, field) {
			t.Fatalf("message %q missing %q", issue.Message, field)
		}
	}
}

// TestDoctorModelInfoIssuesNoProvider 验证未配置 provider/model 时静默跳过：
// 缺失配置文件、空配置、只有无关键的配置都不产生 issue，从而保持 doctor
// 默认输出逐字节不变、golden 不破。
func TestDoctorModelInfoIssuesNoProvider(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "absent.json")
	empty := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(empty, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(dir, "unrelated.json")
	if err := os.WriteFile(unrelated, []byte(`{"baseURL":"https://api.example.test/v1"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{missing, empty, unrelated} {
		ioState, _, _ := newTestIO("", nil)
		if issues := doctorModelInfoIssues(App{IO: ioState, ConfigPath: path}); len(issues) != 0 {
			t.Fatalf("path %q produced issues: %+v", path, issues)
		}
	}
}

// TestDoctorModelInfoIssuesCJK 验证中文 provider 名与中文模型名被原样保留，
// 且 CJK 视觉标记命中后 image=true；消息经脱敏后 CJK 字节不被破坏。
func TestDoctorModelInfoIssuesCJK(t *testing.T) {
	// CJK 来源走环境变量同样应被画像识别。
	envIO, _, _ := newTestIO("", map[string]string{
		"ARGENT_PROVIDER": "文心一言",
		"ARGENT_MODEL":    "文心一言-多模态",
	})
	issues := doctorModelInfoIssues(App{IO: envIO})
	if len(issues) != 1 {
		t.Fatalf("issues = %d, want 1: %+v", len(issues), issues)
	}
	message := issues[0].Message
	for _, want := range []string{"provider=文心一言", "model=文心一言-多模态", "image=true", "tools=true"} {
		if !strings.Contains(message, want) {
			t.Fatalf("message %q missing %q", message, want)
		}
	}
}

// TestDoctorModelInfoEnvOverridesConfig 验证环境变量优先于配置文件，与
// config show 的覆盖优先级保持一致。
func TestDoctorModelInfoEnvOverridesConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path,
		[]byte(`{"provider":"anthropic","model":"claude-3-opus"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ioState, _, _ := newTestIO("", map[string]string{
		"ARGENT_PROVIDER": "openai",
		"ARGENT_MODEL":    "gpt-4o",
	})
	providerName, model := doctorCurrentModel(App{IO: ioState, ConfigPath: path})
	if providerName != "openai" || model != "gpt-4o" {
		t.Fatalf("current model = (%q, %q), want (openai, gpt-4o)", providerName, model)
	}
}

// TestDoctorModelInfoTextOutput 端到端验证画像诊断走 issues 旁路：文本输出含
// warnings 段与 modelInfo 行，四字段齐全，且退出码仍为 0（不改变失败计数）。
func TestDoctorModelInfoTextOutput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path,
		[]byte(`{"provider":"anthropic","model":"claude-3-opus"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ioState, out, _ := newTestIO("", nil)
	app := App{IO: ioState, StorageDir: dir, ConfigPath: path}
	if code := RunWith(app, []string{"doctor", "--base-url", doctorVerifyRefusedURL}); code != 0 {
		t.Fatalf("exit = %d, want 0; out:\n%s", code, out.String())
	}
	text := out.String()
	for _, want := range []string{
		"warnings:", "modelInfo",
		"thinking=true", "image=true", "tools=true", "contextWindow=200000",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("text output missing %q:\n%s", want, text)
		}
	}
	// 画像诊断不是新的 check：检查段仍以原来的 9 项收尾于 go。
	if !strings.Contains(text, "ok   go") {
		t.Fatalf("text output lost go check:\n%s", text)
	}
}

// TestDoctorModelInfoJSONStructureIntact 端到端验证加入画像诊断后 --json 的
// 结构不破：仍是 9 个 check、零 failures、issues 为数组且含一条 modelInfo，
// 字段仅 name/level/message。
func TestDoctorModelInfoJSONStructureIntact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path,
		[]byte(`{"provider":"anthropic","model":"claude-3-opus"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ioState, out, _ := newTestIO("", nil)
	app := App{IO: ioState, StorageDir: dir, ConfigPath: path}
	if code := RunWith(app, []string{"doctor", "--json", "--base-url", doctorVerifyRefusedURL}); code != 0 {
		t.Fatalf("exit = %d, want 0; out:\n%s", code, out.String())
	}

	var report struct {
		Checks   []map[string]any `json:"checks"`
		Failures int              `json:"failures"`
		Issues   []map[string]any `json:"issues"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("不是合法 JSON: %v\n%s", err, out.String())
	}
	if len(report.Checks) != 9 || report.Failures != 0 {
		t.Fatalf("checks = %d, failures = %d, want 9/0: %s", len(report.Checks), report.Failures, out.String())
	}
	if len(report.Issues) != 1 {
		t.Fatalf("issues = %d, want 1: %s", len(report.Issues), out.String())
	}
	issue := report.Issues[0]
	if len(issue) != 3 {
		t.Fatalf("issue 字段数 = %d, want 3: %v", len(issue), issue)
	}
	if issue["name"] != doctorModelInfoIssueName || issue["level"] != statusWarn {
		t.Fatalf("issue = %v, want warn/%s", issue, doctorModelInfoIssueName)
	}
	message, _ := issue["message"].(string)
	for _, field := range []string{"thinking=true", "image=true", "tools=true", "contextWindow=200000"} {
		if !strings.Contains(message, field) {
			t.Fatalf("message %q missing %q", message, field)
		}
	}
}
