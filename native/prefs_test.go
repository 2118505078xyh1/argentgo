package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestNativePrefsRoundTrip(t *testing.T) {
	home := t.TempDir()
	p := nativePrefs{Theme: nativeThemeLight, Locale: nativeLocaleEnUS}
	p.Remember("bash|rm /tmp/x")
	changed := p.Remember("bash|rm /tmp/x")
	if changed {
		t.Fatal("Remember must deduplicate")
	}
	if err := saveNativePrefs(home, p); err != nil {
		t.Fatalf("saveNativePrefs: %v", err)
	}

	got := loadNativePrefs(home)
	if !got.LightTheme() || got.themeCode() != nativeThemeLight {
		t.Fatalf("theme = %q, want light", got.Theme)
	}
	if got.LocaleCode() != nativeLocaleEnUS || !got.hasLocale() {
		t.Fatalf("locale = %q, want en-US", got.Locale)
	}
	if !got.HasAllow("bash|rm /tmp/x") {
		t.Fatalf("allow list not persisted: %+v", got.AllowAlways)
	}
	if got.HasAllow("bash|other") {
		t.Fatalf("unknown pattern unexpectedly allowed: %+v", got.AllowAlways)
	}
}

func TestNativePrefsMissingAndCorruptFallBackToZero(t *testing.T) {
	home := t.TempDir()
	if got := loadNativePrefs(home); got.LightTheme() || got.hasLocale() || len(got.AllowAlways) != 0 {
		t.Fatalf("missing file should yield zero prefs: %+v", got)
	}

	path := nativePrefsPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := loadNativePrefs(home); got.LightTheme() || got.hasLocale() || len(got.AllowAlways) != 0 {
		t.Fatalf("corrupt file should yield zero prefs: %+v", got)
	}
}

func TestSaveNativePrefsIsAtomicAndPrivate(t *testing.T) {
	home := t.TempDir()
	if err := saveThemePref(home, true); err != nil {
		t.Fatalf("saveThemePref: %v", err)
	}
	if err := saveLocalePref(home, "en-US"); err != nil {
		t.Fatalf("saveLocalePref: %v", err)
	}
	path := nativePrefsPath(home)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read prefs: %v", err)
	}
	got := loadNativePrefs(home)
	if !got.LightTheme() || got.LocaleCode() != nativeLocaleEnUS {
		t.Fatalf("prefs after two saves = %+v", got)
	}
	// 临时文件必须被清理，否则会在偏好目录留下垃圾。
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != nativePrefsFileName {
			t.Fatalf("leftover temp file in prefs dir: %q", e.Name())
		}
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(path); info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("prefs file is group/world accessible: %v", info.Mode())
		}
	}
	if len(data) == 0 {
		t.Fatal("prefs file is empty")
	}
}

func TestSaveLocalePrefInvalidClears(t *testing.T) {
	home := t.TempDir()
	if err := saveLocalePref(home, "en-US"); err != nil {
		t.Fatal(err)
	}
	if err := saveLocalePref(home, "fr-FR"); err != nil {
		t.Fatal(err)
	}
	if got := loadNativePrefs(home); got.hasLocale() {
		t.Fatalf("invalid locale should clear preference: %+v", got)
	}
}

func TestNativePrefsHomeEmptyIsSafe(t *testing.T) {
	if got := nativePrefsPath(""); got != "" {
		t.Fatalf("nativePrefsPath(\"\") = %q, want empty", got)
	}
	if err := saveNativePrefs("", nativePrefs{}); err == nil {
		t.Fatal("saving with no home must fail cleanly")
	}
	if got := loadNativePrefs(""); got.LightTheme() {
		t.Fatal("loading with no home must yield zero prefs")
	}
}

func TestConfigureLocaleWithPrefs(t *testing.T) {
	prev := nativeLocale()
	t.Cleanup(func() { nativeLangCode.Store(prev) })

	envEN := func(string) string { return nativeLocaleEnUS }
	// 偏好显式设置时优先于注入的 env，使设置面板的修改跨重启生效。
	configureLocaleWithPrefs(envEN, nativePrefs{Locale: nativeLocaleZhCN})
	if got := nativeLocale(); got != nativeLocaleZhCN {
		t.Fatalf("prefs locale should win: %q", got)
	}
	// 偏好为空时回退到 env。
	configureLocaleWithPrefs(envEN, nativePrefs{})
	if got := nativeLocale(); got != nativeLocaleEnUS {
		t.Fatalf("env locale should be fallback: %q", got)
	}
}

func TestForgetAllowPattern(t *testing.T) {
	home := t.TempDir()
	if err := rememberAllowPattern(home, "bash|ls"); err != nil {
		t.Fatal(err)
	}
	if err := rememberAllowPattern(home, "bash|ls"); err != nil {
		t.Fatal(err)
	}
	if got := loadNativePrefs(home); len(got.AllowAlways) != 1 {
		t.Fatalf("allow list = %+v", got.AllowAlways)
	}
	if err := forgetAllowPattern(home, "bash|ls"); err != nil {
		t.Fatal(err)
	}
	if got := loadNativePrefs(home); got.HasAllow("bash|ls") {
		t.Fatalf("pattern not removed: %+v", got.AllowAlways)
	}
	if err := forgetAllowPattern(home, "bash|ls"); err != nil {
		t.Fatal(err)
	}
}
