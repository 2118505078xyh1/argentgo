package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"example.com/argentgo/internal/config"
	"example.com/argentgo/internal/llm"
)

// initTestApp 构造一个"有交互终端"的测试 App，stdin 由脚本输入驱动。
func initTestApp(t *testing.T, in string) (App, *bytes.Buffer, *bytes.Buffer, string) {
	t.Helper()
	dir := t.TempDir()
	stdio, out, errOut := newTestIO(in, nil)
	stdio.HasTerminal = func() bool { return true }
	stdio.StdinIsCharDevice = func() bool { return true }
	return App{IO: stdio, StorageDir: dir}, out, errOut, dir
}

// stubInitSeams 屏蔽本地端口探测与真实 doctor 网络探测；测试结束恢复原值，
// 避免污染同包其它用例。
func stubInitSeams(t *testing.T) {
	t.Helper()
	oldProbe, oldDoctor, oldDevice := initProbe, initDoctor, initDeviceLogin
	initProbe = func(string, time.Duration) error { return nil }
	initDoctor = func(App, context.Context, []string) int { return 0 }
	initDeviceLogin = func(App, context.Context, string) int { return 0 }
	t.Cleanup(func() {
		initProbe, initDoctor, initDeviceLogin = oldProbe, oldDoctor, oldDevice
	})
}

func configPathIn(dir string) string { return filepath.Join(dir, "config.json") }

// initConfigEnv 让 config.Load 把测试临时目录当作存储根，从而读到 init 写出的文件。
func initConfigEnv(dir string) func(string) string {
	return func(key string) string {
		switch key {
		case "HOME", "USERPROFILE", "ARGENT_STORAGE":
			return dir
		}
		return ""
	}
}

func TestInitRejectsNonTTY(t *testing.T) {
	dir := t.TempDir()
	stdio, _, errOut := newTestIO("1\n", nil)
	stdio.HasTerminal = func() bool { return false }
	app := App{IO: stdio, StorageDir: dir}

	if code := RunWith(app, []string{"init"}); code == 0 {
		t.Fatalf("非 TTY 退出码 = 0, want 非 0")
	}
	if !strings.Contains(errOut.String(), "需要交互终端") {
		t.Fatalf("缺少中文非 TTY 报错: %q", errOut.String())
	}
	if _, err := os.Stat(configPathIn(dir)); !os.IsNotExist(err) {
		t.Fatalf("非 TTY 不应写配置: %v", err)
	}
}

func TestInitNonCharDeviceStdinRejects(t *testing.T) {
	dir := t.TempDir()
	stdio, _, errOut := newTestIO("1\n", nil)
	stdio.HasTerminal = func() bool { return true }
	stdio.StdinIsCharDevice = func() bool { return false }
	app := App{IO: stdio, StorageDir: dir}

	if code := RunWith(app, []string{"init"}); code == 0 {
		t.Fatalf("管道 stdin 退出码 = 0, want 非 0")
	}
	if !strings.Contains(errOut.String(), "需要交互终端") {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

func TestInitSkipWritesNothing(t *testing.T) {
	stubInitSeams(t)
	app, out, _, dir := initTestApp(t, "4\n") // 跳过

	if code := RunWith(app, []string{"init"}); code != 0 {
		t.Fatalf("跳过退出码 = %d, want 0; out %q", code, out.String())
	}
	if !strings.Contains(out.String(), "已跳过初始化") {
		t.Fatalf("out = %q", out.String())
	}
	if _, err := os.Stat(configPathIn(dir)); !os.IsNotExist(err) {
		t.Fatalf("跳过不应写配置: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, firstRunMarkerName)); !os.IsNotExist(err) {
		t.Fatalf("跳过不应写 onboarded 标记: %v", err)
	}
}

func TestInitEOFWritesNothing(t *testing.T) {
	stubInitSeams(t)
	app, out, _, dir := initTestApp(t, "") // 管道结束

	if code := RunWith(app, []string{"init"}); code != 0 {
		t.Fatalf("EOF 退出码 = %d, want 0; out %q", code, out.String())
	}
	if _, err := os.Stat(configPathIn(dir)); !os.IsNotExist(err) {
		t.Fatalf("EOF 不应写配置: %v", err)
	}
}

func TestInitGeneratesCommentedConfigForLocalProvider(t *testing.T) {
	stubInitSeams(t)
	app, out, errOut, dir := initTestApp(t, "1\n\n") // 选 Ollama，回车确认
	code := RunWith(app, []string{"init"})
	if code != 0 {
		t.Fatalf("退出码 = %d, err %q", code, errOut.String())
	}
	for _, want := range []string{"[1/4]", "[2/4]", "[3/4]", "[4/4]", "无需 API key", "完成"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("输出缺少 %q: %q", want, out.String())
		}
	}

	raw, err := os.ReadFile(configPathIn(dir))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "// 本文件由 argentgo init") {
		t.Fatalf("配置缺少注释: %q", raw)
	}
	cfg, err := loadConfigFile(configPathIn(dir))
	if err != nil {
		t.Fatal(err)
	}
	if cfg["provider"] != "ollama" || cfg["model"] != "llama3.2" ||
		cfg["baseURL"] != "http://127.0.0.1:11434/v1" {
		t.Fatalf("生成配置 = %#v", cfg)
	}
	// 生成结果必须能被主配置加载器解码。
	loaded, err := config.Load(dir, initConfigEnv(dir))
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if loaded.ProviderName != "ollama" || loaded.Model != "llama3.2" ||
		loaded.BaseURL != "http://127.0.0.1:11434/v1" {
		t.Fatalf("解码配置 = %+v", loaded)
	}
}

// TestInitPreservesExistingConfig 是"只补缺失项"的 golden 断言：
// 已有 model/baseUrl 别名、自定义 maxTurns 与 providers 条目都逐字节保留。
func TestInitPreservesExistingConfig(t *testing.T) {
	stubInitSeams(t)
	app, _, errOut, dir := initTestApp(t, "1\n\n")
	existing := `{
  "model": "my-model",
  "baseUrl": "http://127.0.0.1:9999/v1",
  "maxTurns": 7,
  "providers": {"ollama": {"apiKey": "keep-me", "extra": true}}
}`
	if err := os.WriteFile(configPathIn(dir), []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := RunWith(app, []string{"init"}); code != 0 {
		t.Fatalf("退出码 = %d, err %q", code, errOut.String())
	}
	cfg, err := loadConfigFile(configPathIn(dir))
	if err != nil {
		t.Fatal(err)
	}
	if cfg["provider"] != "ollama" {
		t.Fatalf("未补 provider: %#v", cfg)
	}
	if cfg["model"] != "my-model" || cfg["baseUrl"] != "http://127.0.0.1:9999/v1" {
		t.Fatalf("覆盖了已有 model/baseUrl: %#v", cfg)
	}
	if _, exists := cfg["baseURL"]; exists {
		t.Fatalf("别名已存在却写入规范键: %#v", cfg)
	}
	if cfg["maxTurns"] != float64(7) {
		t.Fatalf("覆盖了 maxTurns: %#v", cfg["maxTurns"])
	}
	providers, _ := cfg["providers"].(map[string]any)
	ollama, _ := providers["ollama"].(map[string]any)
	if ollama["apiKey"] != "keep-me" || ollama["extra"] != true {
		t.Fatalf("覆盖了 providers.ollama: %#v", ollama)
	}
}

func TestInitStoresAPIKeyWithoutEcho(t *testing.T) {
	stubInitSeams(t)
	app, out, errOut, dir := initTestApp(t, "3\n1\nsk-very-secret\n\n") // 云端 → API key
	if code := RunWith(app, []string{"init"}); code != 0 {
		t.Fatalf("退出码 = %d, err %q", code, errOut.String())
	}
	if strings.Contains(out.String(), "sk-very-secret") || strings.Contains(errOut.String(), "sk-very-secret") {
		t.Fatalf("API key 被回显: out %q err %q", out.String(), errOut.String())
	}
	cfg, err := loadConfigFile(configPathIn(dir))
	if err != nil {
		t.Fatal(err)
	}
	providers, _ := cfg["providers"].(map[string]any)
	openai, _ := providers["openai"].(map[string]any)
	if openai["apiKey"] != "sk-very-secret" {
		t.Fatalf("key 未写入: %#v", cfg)
	}
	if cfg["provider"] != "openai" {
		t.Fatalf("provider = %#v", cfg["provider"])
	}
}

func TestInitAPIKeyAliasNotDuplicated(t *testing.T) {
	stubInitSeams(t)
	app, _, errOut, dir := initTestApp(t, "3\n1\nsk-new\n\n")
	existing := `{"providers": {"openai": {"api_key": "sk-existing"}}}`
	if err := os.WriteFile(configPathIn(dir), []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := RunWith(app, []string{"init"}); code != 0 {
		t.Fatalf("退出码 = %d, err %q", code, errOut.String())
	}
	cfg, err := loadConfigFile(configPathIn(dir))
	if err != nil {
		t.Fatal(err)
	}
	providers, _ := cfg["providers"].(map[string]any)
	openai, _ := providers["openai"].(map[string]any)
	if openai["api_key"] != "sk-existing" {
		t.Fatalf("覆盖了已有 api_key 别名: %#v", openai)
	}
	if _, exists := openai["apiKey"]; exists {
		t.Fatalf("别名已存在却写入规范键: %#v", openai)
	}
}

func TestInitDeclineWriteKeepsDiskUntouched(t *testing.T) {
	stubInitSeams(t)
	app, out, _, dir := initTestApp(t, "1\nn\n")
	if code := RunWith(app, []string{"init"}); code != 0 {
		t.Fatalf("退出码 = %d", code)
	}
	if !strings.Contains(out.String(), "已跳过配置写入") {
		t.Fatalf("out = %q", out.String())
	}
	if _, err := os.Stat(configPathIn(dir)); !os.IsNotExist(err) {
		t.Fatalf("拒绝确认却写了配置: %v", err)
	}
}

// TestInitConfigWorksWithScriptedProvider 覆盖验收主例：向导生成的配置能被
// 主加载器解码，且其中的 baseURL 指向脚本化 provider 时真的能完成一次调用。
func TestInitConfigWorksWithScriptedProvider(t *testing.T) {
	stubInitSeams(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","created":0,"model":"llama3.2",` +
			`"choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	app, _, errOut, dir := initTestApp(t, "1\n\n")
	seed := map[string]any{"baseURL": server.URL}
	data, err := json.Marshal(seed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPathIn(dir), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := RunWith(app, []string{"init"}); code != 0 {
		t.Fatalf("退出码 = %d, err %q", code, errOut.String())
	}

	cfg, err := config.Load(dir, initConfigEnv(dir))
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if cfg.BaseURL != server.URL || cfg.ProviderName != "ollama" {
		t.Fatalf("配置未保留脚本化端点: baseURL=%q provider=%q", cfg.BaseURL, cfg.ProviderName)
	}
	client := llm.New(cfg.BaseURL, "")
	reply, err := client.ChatOnce(context.Background(), cfg.Model,
		[]llm.Message{{Role: "user", Content: "ping"}}, nil)
	if err != nil {
		t.Fatalf("脚本化 provider 调用失败: %v", err)
	}
	if reply.Content != "pong" {
		t.Fatalf("回复 = %q, want pong", reply.Content)
	}
}

func TestInitHelpDoesNotNeedTTY(t *testing.T) {
	stdio, out, _ := newTestIO("", nil)
	stdio.HasTerminal = func() bool { return false }
	app := App{IO: stdio, StorageDir: t.TempDir()}
	if code := RunWith(app, []string{"init", "--help"}); code != 0 {
		t.Fatalf("退出码 = %d", code)
	}
	if !strings.Contains(out.String(), "usage: argentgo init") {
		t.Fatalf("out = %q", out.String())
	}
}
