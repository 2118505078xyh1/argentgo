package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/auth"
)

// 首次运行引导的测试统一采用：临时 StorageDir + 空 stdin + 非 TTY。
// 这样 runMain 读取空 stdin 后按原行为打印 usage 并以 2 退出；
// 引导若有输出，会作为四行中文追加在 usage 之前，便于逐字节对照。
var firstRunGuideLines = [...]string{firstRunLoginLine, firstRunTUILine, firstRunDoctorLine, firstRunInitLine}

func firstRunApp(t *testing.T, dir string, env map[string]string, charDevice bool) (App, *bytes.Buffer) {
	t.Helper()
	stdio, _, errOut := newTestIO("", env)
	stdio.HasTerminal = func() bool { return false }
	if charDevice {
		stdio.StdinIsCharDevice = func() bool { return true }
	}
	return App{IO: stdio, StorageDir: dir}, errOut
}

func writeFirstRunConfig(t *testing.T, dir string) {
	t.Helper()
	cfg := filepath.Join(dir, "config.json")
	data := []byte("{\"providers\":{\"openai\":{\"apiKey\":\"sk-first-run\"}}}\n")
	if err := os.WriteFile(cfg, data, 0o600); err != nil {
		t.Fatalf("写入配置失败: %v", err)
	}
}

// TestFirstRunGuideShowsWhenUnconfigured 覆盖验收主例：无配置 + opt-in +
// 字符设备 stdin + 空 stdin 时，stderr 含三行 login/tui/doctor 引导，
// 且仅在 storage 目录写一次 onboarded 标记。
func TestFirstRunGuideShowsWhenUnconfigured(t *testing.T) {
	dir := t.TempDir()
	app, errOut := firstRunApp(t, dir, map[string]string{optInEnvVar: "cli_hints"}, true)

	if code := RunWith(app, nil); code != 2 {
		t.Fatalf("exit = %d, want 2 (原行为); stderr = %q", code, errOut.String())
	}
	got := errOut.String()
	for _, line := range firstRunGuideLines {
		if !strings.Contains(got, line) {
			t.Errorf("stderr 缺少引导行 %q: %q", line, got)
		}
	}
	for _, needle := range []string{"login openai", "argentgo-native", "argentgo doctor"} {
		if !strings.Contains(got, needle) {
			t.Errorf("stderr 缺少关键字 %q: %q", needle, got)
		}
	}
	// 原行为（usage）仍在新输出中，引导只是增量信息。
	if !strings.Contains(got, "usage: argentgo") {
		t.Errorf("stderr 缺少原 usage: %q", got)
	}
	marker := filepath.Join(dir, firstRunMarkerName)
	if info, err := os.Stat(marker); err != nil {
		t.Fatalf("onboarded 标记未写入 %s: %v", marker, err)
	} else if info.IsDir() {
		t.Fatalf("onboarded 标记 %s 不应是目录", marker)
	}
}

// TestFirstRunGuideSkippedWhenMarked 确认 onboarded 标记存在时不再引导。
func TestFirstRunGuideSkippedWhenMarked(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, firstRunMarkerName)
	if err := os.WriteFile(marker, []byte("already shown\n"), 0o600); err != nil {
		t.Fatalf("预置标记失败: %v", err)
	}
	app, errOut := firstRunApp(t, dir, map[string]string{optInEnvVar: "cli_hints"}, true)

	if code := RunWith(app, nil); code != 2 {
		t.Fatalf("exit = %d, want 2; stderr = %q", code, errOut.String())
	}
	for _, line := range firstRunGuideLines {
		if strings.Contains(errOut.String(), line) {
			t.Fatalf("标记已存在却仍输出引导 %q: %q", line, errOut.String())
		}
	}
}

// TestFirstRunGuideSkippedWhenConfigured 覆盖验收的"带 key 配置"反例：
// 预置带 key 配置时输出与未触发引导的基线逐字节一致，且不写标记。
func TestFirstRunGuideSkippedWhenConfigured(t *testing.T) {
	dir := t.TempDir()
	writeFirstRunConfig(t, dir)
	env := map[string]string{optInEnvVar: "cli_hints"}

	app, errOut := firstRunApp(t, dir, env, true)
	if code := RunWith(app, nil); code != 2 {
		t.Fatalf("exit = %d, want 2; stderr = %q", code, errOut.String())
	}
	// 基线：同样 opt-in、同样配置，但 stdin 不是字符设备，引导绝不触发。
	baseApp, baseErr := firstRunApp(t, dir, env, false)
	if code := RunWith(baseApp, nil); code != 2 {
		t.Fatalf("baseline exit = %d, want 2; stderr = %q", code, baseErr.String())
	}
	if errOut.String() != baseErr.String() {
		t.Fatalf("带 key 配置时输出与基线不一致:\n got = %q\nwant = %q", errOut.String(), baseErr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, firstRunMarkerName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("已配置时不应写 onboarded 标记, stat = %v", err)
	}
}

// TestFirstRunGuideSkippedWhenCredentialStored 覆盖"无凭据"的另一半：
// auth store 中已有 token 时同样视为老用户，不引导。
func TestFirstRunGuideSkippedWhenCredentialStored(t *testing.T) {
	dir := t.TempDir()
	store, err := auth.Open(filepath.Join(dir, "auth"))
	if err != nil {
		t.Fatalf("打开 auth store 失败: %v", err)
	}
	if err := store.Put(auth.Token{Provider: "openai", Kind: auth.KindAPIKey, Access: "sk-stored"}); err != nil {
		t.Fatalf("写入 token 失败: %v", err)
	}
	app, errOut := firstRunApp(t, dir, map[string]string{optInEnvVar: "cli_hints"}, true)

	if code := RunWith(app, nil); code != 2 {
		t.Fatalf("exit = %d, want 2; stderr = %q", code, errOut.String())
	}
	for _, line := range firstRunGuideLines {
		if strings.Contains(errOut.String(), line) {
			t.Fatalf("已有凭据却仍输出引导 %q: %q", line, errOut.String())
		}
	}
}

// TestFirstRunGuideDisabledByEnvZero 覆盖验收反例：策略变更后引导默认开启，
// 只有显式 ARGENT_CLI_HINTS=0 才按原行为退出，输出恰好是 usage，且不写标记、
// 不触发任何探测。
func TestFirstRunGuideDisabledByEnvZero(t *testing.T) {
	dir := t.TempDir()
	app, errOut := firstRunApp(t, dir, map[string]string{cliHintsEnvVar: "0"}, true)

	if code := RunWith(app, nil); code != 2 {
		t.Fatalf("exit = %d, want 2; stderr = %q", code, errOut.String())
	}
	var want bytes.Buffer
	writeUsage(&want)
	if errOut.String() != want.String() {
		t.Fatalf("门控关闭时输出与 usage 不一致:\n got = %q\nwant = %q", errOut.String(), want.String())
	}
	if _, err := os.Stat(filepath.Join(dir, firstRunMarkerName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("门控关闭时不应写 onboarded 标记, stat = %v", err)
	}
}

// TestFirstRunGuideRequiresCharDevice 确认 stdin 非字符设备（管道/重定向）
// 时不引导，即使 opt-in 已开启。
func TestFirstRunGuideRequiresCharDevice(t *testing.T) {
	dir := t.TempDir()
	app, errOut := firstRunApp(t, dir, map[string]string{optInEnvVar: "cli_hints"}, false)

	if code := RunWith(app, nil); code != 2 {
		t.Fatalf("exit = %d, want 2; stderr = %q", code, errOut.String())
	}
	for _, line := range firstRunGuideLines {
		if strings.Contains(errOut.String(), line) {
			t.Fatalf("非字符设备却输出引导 %q: %q", line, errOut.String())
		}
	}
}

// TestFirstRunGuideShowsByDefault 确认策略变更后无需任何 opt-in：无配置 +
// 字符设备 stdin + 空 stdin 即显示三行引导（与错误建议共用 cliHintsEnabled 单一真源）。
func TestFirstRunGuideShowsByDefault(t *testing.T) {
	dir := t.TempDir()
	app, errOut := firstRunApp(t, dir, nil, true)

	if code := RunWith(app, nil); code != 2 {
		t.Fatalf("exit = %d, want 2; stderr = %q", code, errOut.String())
	}
	got := errOut.String()
	for _, line := range firstRunGuideLines {
		if !strings.Contains(got, line) {
			t.Errorf("stderr 缺少引导行 %q: %q", line, got)
		}
	}
}

// TestFirstRunGuideViaIndependentEnv 确认独立变量 ARGENT_CLI_HINTS=1 也复用
// 同一开关开启引导（与错误建议共用的 cliHintsEnabled 单一真源）。
func TestFirstRunGuideViaIndependentEnv(t *testing.T) {
	dir := t.TempDir()
	app, errOut := firstRunApp(t, dir, map[string]string{cliHintsEnvVar: "1"}, true)

	if code := RunWith(app, nil); code != 2 {
		t.Fatalf("exit = %d, want 2; stderr = %q", code, errOut.String())
	}
	for _, line := range firstRunGuideLines {
		if !strings.Contains(errOut.String(), line) {
			t.Fatalf("stderr 缺少引导行 %q: %q", line, errOut.String())
		}
	}
}

// TestFirstRunGuideCJKIntact 确认四行中文以完整 UTF-8 写入，没有按字节截断
// 或宽度填充；CJK 文案逐字节匹配预期。
func TestFirstRunGuideCJKIntact(t *testing.T) {
	dir := t.TempDir()
	app, errOut := firstRunApp(t, dir, map[string]string{optInEnvVar: "cli_hints"}, true)

	if code := RunWith(app, nil); code != 2 {
		t.Fatalf("exit = %d, want 2; stderr = %q", code, errOut.String())
	}
	got := errOut.String()
	if !utf8.ValidString(got) {
		t.Fatalf("stderr 含非法 UTF-8 字节: %q", got)
	}
	for _, line := range firstRunGuideLines {
		if !strings.Contains(got, line) {
			t.Fatalf("CJK 引导行不完整 %q: %q", line, got)
		}
	}
}
