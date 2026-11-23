package main

import (
	"strings"
	"testing"
)

// withLocale 在测试期间切换语言，结束后恢复中文，避免污染其他用例的全局状态。
func withLocale(t *testing.T, env map[string]string) {
	t.Helper()
	configureLocale(func(k string) string { return env[k] })
	t.Cleanup(func() { configureLocale(nil) })
}

func TestNativeLocaleIsOptIn(t *testing.T) {
	// 默认（未显式请求英文）一律保持中文，行为逐字节不变。
	for _, tc := range []struct {
		name string
		env  map[string]string
	}{
		{"unset", map[string]string{}},
		{"empty", map[string]string{"ARGENT_LOCALE": ""}},
		{"zh-CN", map[string]string{"ARGENT_LOCALE": "zh-CN"}},
		{"zh", map[string]string{"ARGENT_LOCALE": "zh"}},
		{"other", map[string]string{"ARGENT_LOCALE": "fr-FR"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withLocale(t, tc.env)
			if got := nativeLocale(); got != nativeLocaleZhCN {
				t.Fatalf("nativeLocale = %q, want zh-CN", got)
			}
			if got := tr("就绪"); got != "就绪" {
				t.Fatalf("tr(就绪) = %q, want unchanged", got)
			}
		})
	}
}

func TestNativeLocaleEnUSUsesSharedCatalogValues(t *testing.T) {
	for _, value := range []string{"en-US", "en", "en_us", "English"} {
		t.Run(value, func(t *testing.T) {
			withLocale(t, map[string]string{"ARGENT_LOCALE": value})
			if got := nativeLocale(); got != nativeLocaleEnUS {
				t.Fatalf("nativeLocale = %q, want en-US", got)
			}
			// 共享键的英文值与 internal/i18n 目录保持一致（内联，避免违规依赖）。
			if got, want := tr("就绪"), "ready"; got != want {
				t.Fatalf("tr(就绪) = %q, want %q", got, want)
			}
			if got := tr("模型"); got != "model" {
				t.Fatalf("tr(模型) = %q", got)
			}
			// 目录暂无对应键的 native 文案走本文件映射。
			if got := tr("定时任务"); got != "Scheduled tasks" {
				t.Fatalf("tr(定时任务) = %q", got)
			}
			// 未登记的文案原样返回，不渲染为空白。
			if got := tr("全新未登记文案"); got != "全新未登记文案" {
				t.Fatalf("tr(unknown) = %q", got)
			}
			// demo 回复与格式串按英文版返回。
			if got := localizedReply(0); !strings.Contains(got, "native Markdown") {
				t.Fatalf("localizedReply(0) = %q", got)
			}
			if got := tr("%d 分钟前"); got != "%d min ago" {
				t.Fatalf("tr(%%d 分钟前) = %q", got)
			}
		})
	}
}
