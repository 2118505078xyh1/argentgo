package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestConfigPathPrintsBothViews 锁定 P1-1 的可观测面：config path 同时打印
// 命令行写入路径与引擎读取路径；当两者一致时不得出现分歧警告。
func TestConfigPathPrintsBothViews(t *testing.T) {
	home := t.TempDir()
	storage := t.TempDir()
	env := map[string]string{
		"HOME":               home,
		"USERPROFILE":        home,
		"ARGENT_STORAGE_DIR": storage,
	}
	ioState, out, errOut := newTestIO("", env)
	code := RunWith(App{IO: ioState}, []string{"config", "path"})
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}

	wantPath := filepath.Join(storage, "config.json")
	for _, want := range []string{
		wantPath,
		"命令行写入路径:  " + wantPath,
		"引擎读取路径（优先级从低到高）:",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("stdout missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(errOut.String(), "警告") {
		t.Errorf("unexpected divergence warning: %q", errOut.String())
	}
}

// TestConfigPathWarnsOnDivergence 覆盖显式 ConfigPath 与引擎候选层不一致的
// 场景：此时用户写入的配置不会生效，必须给出明确警告。
func TestConfigPathWarnsOnDivergence(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home, "USERPROFILE": home}
	ioState, out, errOut := newTestIO("", env)
	oddPath := filepath.Join(t.TempDir(), "elsewhere.json")
	code := RunWith(App{IO: ioState, ConfigPath: oddPath}, []string{"config", "path"})
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), oddPath) {
		t.Errorf("stdout missing write path:\n%s", out.String())
	}
	if !strings.Contains(errOut.String(), "警告") || !strings.Contains(errOut.String(), oddPath) {
		t.Errorf("missing divergence warning: %q", errOut.String())
	}
}

// TestConfigShowPrintsBothViewsAndDoesNotLeakSecrets 确保 config show 的文本与
// JSON 输出都带上两条路径视图，且继续对密钥脱敏。
func TestConfigShowPrintsBothViewsAndDoesNotLeakSecrets(t *testing.T) {
	home := t.TempDir()
	storage := t.TempDir()
	work := t.TempDir()
	configPath := filepath.Join(storage, "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{"model":"stored","apiKey":"sk-secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"HOME":               home,
		"USERPROFILE":        home,
		"ARGENT_STORAGE_DIR": storage,
	}

	ioState, out, errOut := newTestIO("", env)
	code := RunWith(App{IO: ioState}, []string{"config", "show", "-C", work})
	if code != 0 {
		t.Fatalf("text exit = %d, stderr = %q", code, errOut.String())
	}
	for _, want := range []string{"配置路径对照:", "命令行写入路径: " + configPath, "引擎读取路径（候选，优先级从低到高）:", configPath + " (已读取)"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("text output missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "sk-secret") {
		t.Errorf("secret leaked into text output:\n%s", out.String())
	}

	ioState, out, errOut = newTestIO("", env)
	code = RunWith(App{IO: ioState}, []string{"config", "show", "-C", work, "--json"})
	if code != 0 {
		t.Fatalf("json exit = %d, stderr = %q", code, errOut.String())
	}
	var report struct {
		CLIWritePath    string   `json:"cliWritePath"`
		EngineReadPaths []string `json:"engineReadPaths"`
		PathWarning     string   `json:"pathWarning"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out.String())
	}
	if report.CLIWritePath != configPath {
		t.Errorf("cliWritePath = %q, want %q", report.CLIWritePath, configPath)
	}
	found := false
	for _, path := range report.EngineReadPaths {
		if path == configPath {
			found = true
		}
	}
	if !found {
		t.Errorf("engineReadPaths = %v, want %q", report.EngineReadPaths, configPath)
	}
	if report.PathWarning != "" {
		t.Errorf("unexpected pathWarning = %q", report.PathWarning)
	}
	if strings.Contains(out.String(), "sk-secret") {
		t.Errorf("secret leaked into JSON output:\n%s", out.String())
	}
}

// TestStorageDirEnvPrecedence 锁定三处路径推导共用的环境变量优先级。
func TestStorageDirEnvPrecedence(t *testing.T) {
	ioState, _, _ := newTestIO("", map[string]string{
		"ARGENT_STORAGE":     "first",
		"ARGENT_STORAGE_DIR": "second",
		"ARGENT_HOME":        "third",
	})
	app := App{IO: ioState}
	if got := app.storageDir(); got != "first" {
		t.Errorf("storageDir = %q, want first", got)
	}
}

// TestConfigShowLabelsStorageSource 保证 config show 不把 $ARGENT_STORAGE
// 误标成 default，便于排查「配了不生效」。
func TestConfigShowLabelsStorageSource(t *testing.T) {
	home := t.TempDir()
	ioState, out, _ := newTestIO("", map[string]string{
		"HOME":           home,
		"ARGENT_STORAGE": filepath.Join(home, "portable"),
	})
	code := RunWith(App{IO: ioState}, []string{"config", "show", "-C", t.TempDir(), "--json"})
	if code != 0 {
		t.Fatalf("exit = %d, out = %q", code, out.String())
	}
	var report struct {
		Sources struct {
			StorageDir string `json:"storageDir"`
		} `json:"sources"`
		EnvOverrides []string `json:"envOverrides"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Sources.StorageDir != "env:ARGENT_STORAGE" {
		t.Errorf("storageDir source = %q, want env:ARGENT_STORAGE", report.Sources.StorageDir)
	}
	found := false
	for _, key := range report.EnvOverrides {
		if key == "ARGENT_STORAGE" {
			found = true
		}
	}
	if !found {
		t.Errorf("envOverrides = %v, want ARGENT_STORAGE", report.EnvOverrides)
	}
}
