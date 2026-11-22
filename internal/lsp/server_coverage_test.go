package lsp

import "testing"

// TestExtensionLanguagesHaveServer 守护两表一致：extensionLanguages 的每个
// 语言 id 都必须有内置服务器，避免 LanguageForPath 对无服务器的扩展名
// 给出"支持"的假信号，让模型白白调用 lsp_* 工具再失败。
func TestExtensionLanguagesHaveServer(t *testing.T) {
	reg := NewRegistry()
	for ext, lang := range extensionLanguages {
		if _, ok := reg.Lookup(lang); !ok {
			t.Errorf("extension %q maps to language %q with no registered server", ext, lang)
		}
	}
}

// TestLanguageForPathUnsupportedFallsBack 确认无服务器的扩展名不会映射到
// 一个有服务器的语言（即不会误触发 lsp 工具）。
func TestLanguageForPathUnsupportedFallsBack(t *testing.T) {
	reg := NewRegistry()
	for _, path := range []string{"notes.md", "data.json", "main.cpp", "conf.yaml"} {
		if _, ok := reg.Lookup(LanguageForPath(path)); ok {
			t.Errorf("unsupported path %q unexpectedly maps to a served language", path)
		}
	}
}
