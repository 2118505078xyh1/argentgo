package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// 原生窗口的跨重启偏好写入 ~/.argentgo/native-prefs.json。文件不存在、
// 损坏或字段缺失时一律回退零值（深色主题 + 中文 + 空允许列表），
// 绝不因偏好读取失败而阻断 UI 启动。
const (
	nativePrefsDirName  = ".argentgo"
	nativePrefsFileName = "native-prefs.json"

	nativeThemeDark  = "dark"
	nativeThemeLight = "light"
)

// nativePrefs 是需要持久化的原生偏好。写回时整份保存，因此新增字段对旧
// 文件兼容（缺失字段为零值）；未知字段由 encoding/json 静默忽略。
type nativePrefs struct {
	Theme       string   `json:"theme,omitempty"`
	Locale      string   `json:"locale,omitempty"`
	AllowAlways []string `json:"allow_always,omitempty"`
}

// LightTheme 报告偏好是否要求浅色主题。零值（含空文件）为深色。
func (p nativePrefs) LightTheme() bool { return p.Theme == nativeThemeLight }

// themeCode 返回可写回的主题代码。
func (p nativePrefs) themeCode() string {
	if p.LightTheme() {
		return nativeThemeLight
	}
	return nativeThemeDark
}

// LocaleCode 返回已规范化的语言代码；未设置或无效时返回空串。
func (p nativePrefs) LocaleCode() string {
	if code, ok := canonicalNativeLocale(p.Locale); ok {
		return code
	}
	return ""
}

// hasLocale 报告偏好是否显式指定了语言。
func (p nativePrefs) hasLocale() bool { return p.LocaleCode() != "" }

// HasAllow 报告某个命令模式是否在"始终允许"列表中。
func (p nativePrefs) HasAllow(pattern string) bool {
	for _, item := range p.AllowAlways {
		if item == pattern {
			return true
		}
	}
	return false
}

// Remember 把命令模式加入"始终允许"列表（去重）；返回新列表是否发生变化。
func (p *nativePrefs) Remember(pattern string) bool {
	if pattern == "" || p.HasAllow(pattern) {
		return false
	}
	p.AllowAlways = append(p.AllowAlways, pattern)
	return true
}

// Forget 从"始终允许"列表移除命令模式；返回是否实际移除。
func (p *nativePrefs) Forget(pattern string) bool {
	for i, item := range p.AllowAlways {
		if item == pattern {
			p.AllowAlways = append(p.AllowAlways[:i], p.AllowAlways[i+1:]...)
			return true
		}
	}
	return false
}

// nativePrefsHome 返回偏好文件所在的用户主目录。获取失败时返回空串，
// 此时所有读写操作都是空操作（读取回零值、写入返回错误）。
func nativePrefsHome() string {
	home, _ := os.UserHomeDir()
	return home
}

// nativePrefsPath 返回偏好文件路径。home 为空时返回空串，避免写入相对路径。
func nativePrefsPath(home string) string {
	if strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, nativePrefsDirName, nativePrefsFileName)
}

// loadNativePrefs 读取偏好文件。任何错误（文件不存在、JSON 损坏、权限不足）
// 都回退零值，使偏好成为纯增强项而非启动依赖。
func loadNativePrefs(home string) nativePrefs {
	path := nativePrefsPath(home)
	if path == "" {
		return nativePrefs{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nativePrefs{}
	}
	var p nativePrefs
	if json.Unmarshal(data, &p) != nil {
		return nativePrefs{}
	}
	return p
}

// saveNativePrefs 原子写回偏好：先写同目录临时文件再 rename，避免进程在
// 写入中途退出时留下半份 JSON。目录与文件都按仅当前用户可访问创建。
func saveNativePrefs(home string, p nativePrefs) error {
	path := nativePrefsPath(home)
	if path == "" {
		return fmt.Errorf("native: cannot locate user home for preferences")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(dir, ".native-prefs-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// saveThemePref 保存主题偏好。
func saveThemePref(home string, light bool) error {
	p := loadNativePrefs(home)
	if light {
		p.Theme = nativeThemeLight
	} else {
		p.Theme = nativeThemeDark
	}
	return saveNativePrefs(home, p)
}

// saveLocalePref 保存语言偏好；无效语言代码视为清除偏好。
func saveLocalePref(home, locale string) error {
	p := loadNativePrefs(home)
	if code, ok := canonicalNativeLocale(locale); ok {
		p.Locale = code
	} else {
		p.Locale = ""
	}
	return saveNativePrefs(home, p)
}

// rememberAllowPattern / forgetAllowPattern 维护"始终允许"命令模式列表，
// 供审批作用域决策在做出决定后持久化。
func rememberAllowPattern(home, pattern string) error {
	if strings.TrimSpace(pattern) == "" {
		return nil
	}
	p := loadNativePrefs(home)
	if !p.Remember(pattern) {
		return nil
	}
	return saveNativePrefs(home, p)
}

func forgetAllowPattern(home, pattern string) error {
	p := loadNativePrefs(home)
	if !p.Forget(pattern) {
		return nil
	}
	return saveNativePrefs(home, p)
}

// configureLocaleWithPrefs 按"偏好优先、环境变量兜底"的顺序设置语言。
// 在设置面板改过语言后，即时写入偏好；下次启动时即使 GUI 仍注入
// ARGENT_LOCALE，用户的选择也优先生效。
func configureLocaleWithPrefs(env func(string) string, prefs nativePrefs) {
	if code := prefs.LocaleCode(); code != "" {
		nativeLangCode.Store(code)
		return
	}
	configureLocale(env)
}
