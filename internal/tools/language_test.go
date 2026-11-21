package tools

import "testing"

// TestDetectLanguageByExtension 覆盖统一检测表中的各类扩展名。
func TestDetectLanguageByExtension(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"main.go", "go"},
		{"app.py", "python"},
		{"script.pyw", "python"},
		{"types.pyi", "python"},
		{"index.js", "javascript"},
		{"component.jsx", "javascript"},
		{"entry.mjs", "javascript"},
		{"bundle.cjs", "javascript"},
		{"main.ts", "typescript"},
		{"view.tsx", "typescript"},
		{"worker.mts", "typescript"},
		{"worker.cts", "typescript"},
		{"lib.rs", "rust"},
		{"run.sh", "shell"},
		{"run.bash", "shell"},
		{"run.zsh", "shell"},
		{"config.json", "json"},
		{"config.jsonc", "json"},
		{"config.yaml", "yaml"},
		{"config.yml", "yaml"},
		{"README.md", "markdown"},
		{"doc.markdown", "markdown"},
		{"page.mdx", "markdown"},
		{"Main.java", "java"},
		{"main.c", "c"},
		{"main.cpp", "cpp"},
		{"Program.cs", "csharp"},
		{"index.html", "html"},
		{"site.css", "css"},
		{"data.xml", "xml"},
		{"Cargo.toml", "toml"},
		{"settings.ini", "ini"},
		{"schema.sql", "sql"},
		{"api.proto", "protobuf"},
		{"main.tf", "terraform"},
		{"script.ps1", "powershell"},
		{"run.bat", "batch"},
		{"service.dockerfile", "dockerfile"},
		{"fix.patch", "diff"},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if got := detectLanguage(tc.path); got != tc.want {
				t.Fatalf("detectLanguage(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

// TestDetectLanguageNoExtension 验证没有扩展名或查不到时返回 unknown。
func TestDetectLanguageNoExtension(t *testing.T) {
	cases := []string{
		"README",
		"Makefile",
		"src/main",
		"unknown.xyz",
		"archive.tar.gz",
	}
	for _, path := range cases {
		t.Run(path, func(t *testing.T) {
			if got := detectLanguage(path); got != "unknown" {
				t.Fatalf("detectLanguage(%q) = %q, want unknown", path, got)
			}
		})
	}
}

// TestDetectLanguageCaseInsensitive 验证扩展名大小写不敏感。
func TestDetectLanguageCaseInsensitive(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"main.GO", "go"},
		{"main.Go", "go"},
		{"APP.PY", "python"},
		{"APP.Py", "python"},
		{"index.JS", "javascript"},
		{"view.TSX", "typescript"},
		{"LIB.RS", "rust"},
		{"CONFIG.YAML", "yaml"},
		{"README.MD", "markdown"},
		{"data.JSON", "json"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if got := detectLanguage(tc.path); got != tc.want {
				t.Fatalf("detectLanguage(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

// TestDetectLanguageCJKFilename 验证含 CJK 字符的文件名不会影响扩展名识别。
func TestDetectLanguageCJKFilename(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"主程序.go", "go"},
		{"配置.环境.yaml", "yaml"},
		{"文档/README.中文.md", "markdown"},
		{"脚本.GO", "go"},
		{"日本語.py", "python"},
		{"中文文件", "unknown"},
		{"中文文件.未知扩展", "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if got := detectLanguage(tc.path); got != tc.want {
				t.Fatalf("detectLanguage(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}
