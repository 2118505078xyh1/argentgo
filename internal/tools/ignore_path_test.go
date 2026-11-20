package tools

import "testing"

// TestIsIgnoredPathPatterns 用表驱动方式覆盖 gitignore 风格模式的各类语义：
// 字面量、通配符、目录、跨段、取反、嵌套路径与空模式。
func TestIsIgnoredPathPatterns(t *testing.T) {
	cases := []struct {
		name     string
		relPath  string
		patterns []string
		want     bool
	}{
		// 空模式或全是注释/空行：不忽略任何内容。
		{"nil patterns", "main.go", nil, false},
		{"empty slice", "main.go", []string{}, false},
		{"blank and comments only", "main.go", []string{"", "   ", "# comment", "  # indented"}, false},
		{"bare negation only", "main.go", []string{"!", "  !  "}, false},

		// 通配符 *.env：在任意深度匹配以 .env 结尾的基础名。
		{"star env root", ".env", []string{"*.env"}, true},
		{"star env nested", "project/src/.env", []string{"*.env"}, true},
		{"star env suffixed name", "config/app.env", []string{"*.env"}, true},
		{"star env does not match bak", "config/.env.bak", []string{"*.env"}, false},
		{"star env does not match plain env", "config/env", []string{"*.env"}, false},

		// 字面量 .env：命中文件名及其后代。
		{"literal env", ".env", []string{".env"}, true},
		{"literal env nested", "config/.env", []string{".env"}, true},
		{"literal env does not match local", "config/.env.local", []string{".env"}, false},

		// 目录模式 dir/：匹配目录本身及其下任意深度路径。
		{"dir pattern matches dir", "secret", []string{"secret/"}, true},
		{"dir pattern matches child", "secret/key.pem", []string{"secret/"}, true},
		{"dir pattern matches deep child", "a/secret/sub/key.pem", []string{"secret/"}, true},
		{"dir pattern does not match prefix name", "secrets.txt", []string{"secret/"}, false},

		// 跨段 **/secret*：在任意深度匹配"最后一段以 secret 开头"的路径。
		{"doublestar root basename", "secret_token", []string{"**/secret*"}, true},
		{"doublestar nested", "a/b/secret_token", []string{"**/secret*"}, true},
		{"doublestar leading dir no match", "secret/key", []string{"**/secret*"}, false},
		{"doublestar no match", "a/b/pubkey", []string{"**/secret*"}, false},

		// 取反 !：最后一条匹配模式胜出。
		{"negation re-includes", "config/.env", []string{"*.env", "!config/.env"}, false},
		{"negation still ignores others", "other/.env", []string{"*.env", "!config/.env"}, true},
		{"later rule overrides negation", "config/.env", []string{"*.env", "!config/.env", "config/.env"}, true},
		{"negation before exclusion order", "config/.env", []string{"!config/.env", "*.env"}, true},

		// CJK：模式与路径都按 rune 比较，不会被字节截断。
		{"cjk literal", "凭证.txt", []string{"凭证.txt"}, true},
		{"cjk nested glob", "密钥/私钥.pem", []string{"密钥/*.pem"}, true},
		{"cjk doublestar", "项目/密钥/私钥.pem", []string{"**/私钥.pem"}, true},
		{"cjk no match", "项目/公钥.pem", []string{"密钥/*.pem"}, false},

		// 相对路径清理：./ 与开头的 / 会被去掉，反斜杠算分隔符。
		{"dot slash prefix", "./config/.env", []string{"*.env"}, true},
		{"leading slash", "/config/.env", []string{"*.env"}, true},
		{"windows separators", `config\.env`, []string{"*.env"}, true},
		{"windows pattern separators", `secret\key.pem`, []string{`secret\`}, true},

		// 大小写不敏感：复用 GlobMatch 的小写化语义。
		{"case insensitive", "Secret.TXT", []string{"secret.txt"}, true},
		{"case insensitive glob", "Config/App.ENV", []string{"*.env"}, true},

		// 首尾 / 被裁剪；单纯的根路径不被忽略。
		{"trimmed slashes", "a/secret/b", []string{"/secret/"}, true},
		{"root path never ignored", ".", []string{"**"}, false},
		{"empty path never ignored", "", []string{"**"}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := append([]string(nil), tc.patterns...)
			got := isIgnoredPath(tc.relPath, tc.patterns)
			if got != tc.want {
				t.Fatalf("isIgnoredPath(%q, %q) = %v, want %v", tc.relPath, tc.patterns, got, tc.want)
			}
			// 纯函数不得修改调用方传入的模式切片。
			for i := range before {
				if before[i] != tc.patterns[i] {
					t.Fatalf("isIgnoredPath mutated patterns[%d]: %q -> %q", i, before[i], tc.patterns[i])
				}
			}
		})
	}
}

// TestIsIgnoredPathBOMAndComments 验证带 BOM 的首行与注释/裸取反混排时的行为。
func TestIsIgnoredPathBOMAndComments(t *testing.T) {
	patterns := []string{
		"\xef\xbb\xbf*.env", // 首行带 BOM，必须被当作正常的 *.env 规则。
		"# 这是注释",
		"",
		"!",
		"!config/.env",
	}
	for _, path := range []string{".env", "config/app.env"} {
		if !isIgnoredPath(path, patterns) {
			t.Fatalf("BOM-prefixed *.env should ignore %q", path)
		}
	}
	if isIgnoredPath("config/.env", patterns) {
		t.Fatal("negated config/.env should not be ignored")
	}
}
