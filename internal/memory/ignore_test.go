package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeIgnoreFile(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, IgnoreFileName)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func loadIgnoreOrFatal(t *testing.T, dir string) *IgnoreRules {
	t.Helper()
	rules, err := LoadIgnore(dir)
	if err != nil {
		t.Fatalf("LoadIgnore(%q): %v", dir, err)
	}
	return rules
}

// 没有规则意味着不排除任何内容，nil 规则集也是如此。
func TestLoadIgnoreNoRulesIgnoresNothing(t *testing.T) {
	dir := t.TempDir()
	rules := loadIgnoreOrFatal(t, dir)
	if len(rules.patterns) != 0 {
		t.Fatalf("unexpected patterns: %+v", rules.patterns)
	}
	for _, path := range []string{"a.go", "src/a.go", ""} {
		if rules.Ignored(path) {
			t.Errorf("Ignored(%q) = true with no rules", path)
		}
	}
	var nilRules *IgnoreRules
	if nilRules.Ignored("anything") {
		t.Error("nil ruleset ignored a path")
	}
}

// 父目录的规则对其子目录生效；子目录中较晚的规则优先，而 `!`
// 重新包含先前模式排除的内容。
func TestLoadIgnoreParentChildPrecedenceAndNegation(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "pkg")
	if err := os.MkdirAll(child, 0o700); err != nil {
		t.Fatal(err)
	}
	writeIgnoreFile(t, root, "*.log\nbuild/\n")
	writeIgnoreFile(t, child, "!keep.log\n*.tmp\n")

	rules := loadIgnoreOrFatal(t, child)
	if got := len(rules.patterns); got != 4 {
		t.Fatalf("got %d patterns, want 4: %+v", got, rules.patterns)
	}
	// 父目录规则先收集，因此子目录规则在后面。
	if !strings.Contains(rules.patterns[0].glob, "log") || !rules.patterns[2].negated {
		t.Fatalf("unexpected pattern order: %+v", rules.patterns)
	}

	cases := []struct {
		path string
		want bool
	}{
		{"debug.log", true},    // parent rule
		{"keep.log", false},    // child `!` overrides parent
		{"build/out.go", true}, // parent directory rule
		{"cache.tmp", true},    // child rule
		{"main.go", false},
	}
	for _, tc := range cases {
		if got := rules.Ignored(tc.path); got != tc.want {
			t.Errorf("Ignored(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// `!` 重新包含先前模式排除的内容；最后一条普通模式
// 又将其排除。总是以最后一条匹配的行为准。
func TestLoadIgnoreLastMatchingPatternWins(t *testing.T) {
	dir := t.TempDir()
	writeIgnoreFile(t, dir, "secret*\n!secret-public.txt\nsecret-public.txt\n")

	rules := loadIgnoreOrFatal(t, dir)
	if !rules.Ignored("secret-key.txt") {
		t.Error("first pattern should ignore secret-key.txt")
	}
	if !rules.Ignored("secret-public.txt") {
		t.Error("last pattern should re-exclude secret-public.txt")
	}
	if rules.Ignored("public.txt") {
		t.Error("unmatched path should not be ignored")
	}
}

// 注释、空行和仅含空白的行不是模式；单独一个
// `!` 会被丢弃，而不是成为匹配所有的重新包含。
func TestLoadIgnoreSkipsCommentsBlanksAndBareBang(t *testing.T) {
	dir := t.TempDir()
	writeIgnoreFile(t, dir, "# a comment\n\n   \n!   \nkeep.go\n")

	rules := loadIgnoreOrFatal(t, dir)
	if len(rules.patterns) != 1 {
		t.Fatalf("got %d patterns, want 1: %+v", len(rules.patterns), rules.patterns)
	}
	if !rules.Ignored("keep.go") || rules.Ignored("other.go") {
		t.Error("only keep.go should be ignored")
	}
}

// Glob 语义来自 security.GlobMatch：`*`、`**` 和 `?`。
func TestLoadIgnoreGlobSemantics(t *testing.T) {
	dir := t.TempDir()
	writeIgnoreFile(t, dir, "*.tmp\nsrc/**/generated.go\ntest?.go\n")

	rules := loadIgnoreOrFatal(t, dir)
	cases := []struct {
		path string
		want bool
	}{
		{"notes.tmp", true},               // `*` matches in a tail
		{"a/b/notes.tmp", true},           // pattern without slash matches any tail
		{"src/parser/generated.go", true}, // `**` crosses segments
		{"src/generated.go", true},        // `**` also matches zero segments
		{"lib/generated.go", false},
		{"test1.go", true}, // `?` matches one character
		{"testx.go", true},
		{"test12.go", false}, // not two characters
		{"src/parser/handwritten.go", false},
	}
	for _, tc := range cases {
		if got := rules.Ignored(tc.path); got != tc.want {
			t.Errorf("Ignored(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// 路径用 filepath.ToSlash 规范化；原生分隔符、前导 `./`
// 和前导 `/` 都解析为同一路径。
func TestLoadIgnorePathNormalization(t *testing.T) {
	dir := t.TempDir()
	writeIgnoreFile(t, dir, "secret.txt\n")

	rules := loadIgnoreOrFatal(t, dir)
	native := filepath.Join("a", "b", "secret.txt")
	for _, path := range []string{"secret.txt", native, "./secret.txt", "/secret.txt"} {
		if !rules.Ignored(path) {
			t.Errorf("Ignored(%q) = false, want true", path)
		}
	}
	if rules.Ignored(filepath.Join("a", "b", "other.txt")) {
		t.Error("unrelated path should not be ignored")
	}
}

// GlobMatch 将两边都转小写，因此忽略匹配在所有平台上都
// 不区分大小写（与安全策略使用的规则相同）。
func TestLoadIgnoreCaseInsensitiveMatching(t *testing.T) {
	dir := t.TempDir()
	writeIgnoreFile(t, dir, "Secret.TXT\nBuild/\n")

	rules := loadIgnoreOrFatal(t, dir)
	for _, path := range []string{"secret.txt", "SECRET.txt", "build/output.o", "BUILD/output.o"} {
		if !rules.Ignored(path) {
			t.Errorf("Ignored(%q) = false, want true (case-insensitive)", path)
		}
	}
}

// .argentignore 文件开头的 UTF-8 BOM 是编码元数据，而非
// 第一行的内容。若不去掉它，第一个模式会变成
// "\ufeff*.log" 而永不匹配；更糟的是，带 BOM 前缀的注释会被误认为
// 模式而非被跳过。指令文件加载器已经
// 去掉前导 BOM；忽略文件也必须这样做。
func TestLoadIgnoreStripsLeadingBOM(t *testing.T) {
	t.Run("before first pattern", func(t *testing.T) {
		dir := t.TempDir()
		writeIgnoreFile(t, dir, "\ufeff*.log\nkeep.txt\n")

		rules := loadIgnoreOrFatal(t, dir)
		if got := len(rules.patterns); got != 2 {
			t.Fatalf("got %d patterns, want 2: %+v", got, rules.patterns)
		}
		if !rules.Ignored("debug.log") {
			t.Error("BOM-prefixed first pattern should ignore debug.log")
		}
		if rules.Ignored("main.go") {
			t.Error("unmatched path should not be ignored")
		}
	})

	t.Run("before leading comment", func(t *testing.T) {
		dir := t.TempDir()
		writeIgnoreFile(t, dir, "\ufeff# a comment\n*.log\n!keep.log\n")

		rules := loadIgnoreOrFatal(t, dir)
		if got := len(rules.patterns); got != 2 {
			t.Fatalf("BOM-prefixed comment became a pattern; got %d patterns, want 2: %+v", got, rules.patterns)
		}
		if !rules.Ignored("debug.log") {
			t.Error("first real pattern should ignore debug.log")
		}
		if rules.Ignored("keep.log") {
			t.Error("negation after the comment should re-include keep.log")
		}
	})
}

// 无效输入是错误；名为 .argentignore 的条目若是目录
// 则直接跳过，使发现保持尽力而为。
func TestLoadIgnoreInvalidDirAndDirectoryEntry(t *testing.T) {
	if _, err := LoadIgnore(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("LoadIgnore on a missing directory should fail")
	}
	file := filepath.Join(t.TempDir(), "not-a-dir.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadIgnore(file); err == nil {
		t.Error("LoadIgnore on a file should fail")
	}

	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, IgnoreFileName), 0o700); err != nil {
		t.Fatal(err)
	}
	rules := loadIgnoreOrFatal(t, dir)
	if rules.Ignored("anything") {
		t.Error("directory named .argentignore should contribute no rules")
	}
}
