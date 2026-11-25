package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"example.com/argentgo/internal/config"
)

// writeReloadConfigRaw 按原始 JSON 写入配置文件，供差异摘要测试编辑任意键。
func writeReloadConfigRaw(t *testing.T, path, raw string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
}

// reloadCfgRaw 从隔离的临时工作区加载一份自定义配置，返回快照与配置文件路径。
func reloadCfgRaw(t *testing.T, raw string) (*config.Config, string) {
	t.Helper()
	home, cwd := t.TempDir(), t.TempDir()
	path := filepath.Join(cwd, "argent.json")
	writeReloadConfigRaw(t, path, raw)
	cfg, err := config.Load(cwd, envFrom(map[string]string{"HOME": home, "USERPROFILE": home}))
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return &cfg, path
}

// TestConfigReloadDiffEnvGate 锁定 =0 精确匹配（不修剪）的解析规则：默认（含空串）
// 开启，只有精确 "0" 才关闭；因此 " 0 " 不算关闭，仍视为开启。
func TestConfigReloadDiffEnvGate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		want  bool
	}{
		{"unset/empty", "", true},
		{"on", "1", true},
		{"true word", "true", true},
		{"zero", "0", false},
		{"zero with spaces", " 0 ", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ARGENT_CONFIG_RELOAD_DIFF", tc.value)
			if got := configReloadDiffEnabled(); got != tc.want {
				t.Fatalf("configReloadDiffEnabled = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestConfigReloadDiffListsChangedKeys 覆盖默认开启时变更键被逐项列出，
// 且敏感键以 <redacted> 占位显示而非原文。
func TestConfigReloadDiffListsChangedKeys(t *testing.T) {
	t.Setenv("ARGENT_CONFIG_RELOAD_DIFF", "")
	cfg, path := reloadCfgRaw(t, `{"model":"old-model","keep":"same"}`)
	var logs bytes.Buffer
	r := newConfigReloader(cfg, func(msg string) { logs.WriteString(msg + "\n") })

	writeReloadConfigRaw(t, path, `{"model":"a-much-longer-new-model","temperature":0.7,"providers":{"x":{"apiKey":"sk-super-secret-abcdef"}}}`)
	if !r.poll() {
		t.Fatal("poll after edit did not adopt")
	}
	got := logs.String()
	for _, want := range []string{"config reloaded:", "model", "temperature", "providers.x.apiKey=<redacted>"} {
		if !strings.Contains(got, want) {
			t.Errorf("摘要缺少 %q: %q", want, got)
		}
	}
	// temperature 不是敏感键，必须不带 =<redacted> 后缀。
	if strings.Contains(got, "temperature=<redacted>") {
		t.Errorf("非敏感键被误脱敏: %q", got)
	}
}

// TestConfigReloadDiffRedactsSecrets 是安全硬约束：默认开启时，
// 敏感键（含嵌套、下划线拼写、复合后缀）的原文绝不出现在摘要中。
func TestConfigReloadDiffRedactsSecrets(t *testing.T) {
	t.Setenv("ARGENT_CONFIG_RELOAD_DIFF", "")
	secrets := []string{
		"sk-live-abcdef123456",
		"ghp_token_9371example",
		"hunter2-db-password",
		"nested-secret-value-xyz",
	}
	initial := `{"model":"m","apiKey":"` + secrets[0] + `","access_token":"` + secrets[1] + `","dbPassword":"` + secrets[2] + `","nested":{"secret":"` + secrets[3] + `"}}`
	updated := `{"model":"a-longer-model-name","apiKey":"` + secrets[0] + `-new","access_token":"` + secrets[1] + `-new","dbPassword":"` + secrets[2] + `-new","nested":{"secret":"` + secrets[3] + `-new"}}`
	cfg, path := reloadCfgRaw(t, initial)
	var logs bytes.Buffer
	r := newConfigReloader(cfg, func(msg string) { logs.WriteString(msg + "\n") })

	writeReloadConfigRaw(t, path, updated)
	if !r.poll() {
		t.Fatal("poll after edit did not adopt")
	}
	got := logs.String()
	for _, secret := range secrets {
		if strings.Contains(got, secret) || strings.Contains(got, secret+"-new") {
			t.Errorf("摘要泄漏了敏感原文 %q: %q", secret, got)
		}
	}
	for _, want := range []string{"apiKey=<redacted>", "access_token=<redacted>", "dbPassword=<redacted>", "nested.secret=<redacted>"} {
		if !strings.Contains(got, want) {
			t.Errorf("摘要缺少脱敏占位 %q: %q", want, got)
		}
	}
}

// TestConfigReloadDiffNoChangeNoOutput 校验默认开启时，无磁盘变更的轮询
// 既不采用快照，也不输出任何摘要。
func TestConfigReloadDiffNoChangeNoOutput(t *testing.T) {
	t.Setenv("ARGENT_CONFIG_RELOAD_DIFF", "")
	cfg, _ := reloadCfgRaw(t, `{"model":"steady"}`)
	var logs bytes.Buffer
	r := newConfigReloader(cfg, func(msg string) { logs.WriteString(msg + "\n") })

	if r.poll() {
		t.Fatal("clean poll reported an adoption")
	}
	if logs.Len() != 0 {
		t.Fatalf("clean poll 不应输出摘要: %q", logs.String())
	}
	if r.snapshot() != cfg {
		t.Fatal("clean poll changed the snapshot")
	}
}

// TestConfigReloadDiffDisabledKeepsBaseline 校验显式 =0 关闭时，
// 日志仍为原有的单行基线格式，不出现差异摘要。
func TestConfigReloadDiffDisabledKeepsBaseline(t *testing.T) {
	t.Setenv("ARGENT_CONFIG_RELOAD_DIFF", "0")
	cfg, path := reloadCfgRaw(t, `{"model":"before-model","apiKey":"plain-secret-value"}`)
	var logs bytes.Buffer
	r := newConfigReloader(cfg, func(msg string) { logs.WriteString(msg + "\n") })

	writeReloadConfigRaw(t, path, `{"model":"a-considerably-longer-after-model","apiKey":"plain-secret-value-new"}`)
	if !r.poll() {
		t.Fatal("poll after edit did not adopt")
	}
	got := logs.String()
	if !strings.Contains(got, "reloaded:") || !strings.Contains(got, "generation 1") || !strings.Contains(got, "generation 2") {
		t.Errorf("=0 关闭后基线日志被改变: %q", got)
	}
	if strings.Contains(got, "config reloaded:") {
		t.Errorf("=0 关闭后仍输出了差异摘要: %q", got)
	}
}

// TestConfigReloadDiffCJKKeys 校验 CJK 键路径在摘要中完整保留、UTF-8 合法，
// 且不会被误判为敏感键（CJK 段名归一化为空）。
func TestConfigReloadDiffCJKKeys(t *testing.T) {
	t.Setenv("ARGENT_CONFIG_RELOAD_DIFF", "")
	cfg, path := reloadCfgRaw(t, `{"model":"m","嵌套":{"键":"旧"}}`)
	var logs bytes.Buffer
	r := newConfigReloader(cfg, func(msg string) { logs.WriteString(msg + "\n") })

	writeReloadConfigRaw(t, path, `{"model":"a-longer-model","嵌套":{"键":"新值"}}`)
	if !r.poll() {
		t.Fatal("poll after edit did not adopt")
	}
	got := logs.String()
	if !strings.Contains(got, "嵌套.键") {
		t.Errorf("CJK 键路径未完整保留: %q", got)
	}
	if strings.Contains(got, "嵌套.键=<redacted>") {
		t.Errorf("CJK 键被误判为敏感: %q", got)
	}
	if !utf8.ValidString(got) {
		t.Errorf("摘要含非法 UTF-8: %q", got)
	}
}

// TestConfigReloadDiffConcurrent 校验默认开启时并发轮询同一重载器安全：
// 多个 goroutine 中至多一个采纳快照，日志写入被重载器互斥锁串行化，无数据竞争。
func TestConfigReloadDiffConcurrent(t *testing.T) {
	t.Setenv("ARGENT_CONFIG_RELOAD_DIFF", "")
	cfg, path := reloadCfgRaw(t, `{"model":"before"}`)
	var logs bytes.Buffer
	r := newConfigReloader(cfg, func(msg string) { logs.WriteString(msg + "\n") })

	writeReloadConfigRaw(t, path, `{"model":"a-considerably-longer-after-model","apiKey":"sk-concurrent-secret"}`)
	const n = 32
	var wg sync.WaitGroup
	adoptions := make([]bool, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			adoptions[i] = r.poll()
		}(i)
	}
	wg.Wait()

	adopted := 0
	for _, ok := range adoptions {
		if ok {
			adopted++
		}
	}
	if adopted != 1 {
		t.Fatalf("并发轮询采纳次数 = %d, want 1", adopted)
	}
	got := logs.String()
	if !strings.Contains(got, "config reloaded:") || !strings.Contains(got, "apiKey=<redacted>") {
		t.Errorf("并发轮询未输出正确摘要: %q", got)
	}
	if strings.Contains(got, "sk-concurrent-secret") {
		t.Errorf("并发摘要泄漏敏感原文: %q", got)
	}
}

// TestReloadDiffSensitiveKeys 直接锁定敏感判定规则与 config 一致：
// 凭据键命中，普通键与 CJK 键不命中。
func TestReloadDiffSensitiveKeys(t *testing.T) {
	for _, key := range []string{
		"apiKey", "api_key", "API-KEY", "token", "accessToken", "dbPassword",
		"nested.secret", "providers.openai.apiKey", "credential", "accessKey", "privateKey",
	} {
		if !reloadDiffSensitive(key) {
			t.Errorf("reloadDiffSensitive(%q) = false, want true", key)
		}
	}
	for _, key := range []string{
		"model", "keep", "monkey", "keyboard", "嵌套.键", "model-name",
	} {
		if reloadDiffSensitive(key) {
			t.Errorf("reloadDiffSensitive(%q) = true, want false", key)
		}
	}
}
