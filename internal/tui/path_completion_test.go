package tui

import (
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// completionTestFS 构造注入式工作区树。所有键经 filepath.Join 生成，
// 保证在 Windows 与 POSIX 上都与被测代码的路径拼接一致；含 CJK 文件名、
// 带空格文件/目录名与需忽略的 .git 目录。
func completionTestFS() (string, fakeFS) {
	root := filepath.Clean(filepath.FromSlash("/work"))
	key := func(parts ...string) string {
		return filepath.Join(append([]string{root}, parts...)...)
	}
	fs := fakeFS{dirs: map[string][]FileInfo{
		root: {
			{Name: "src", IsDir: true},
			{Name: "my project", IsDir: true},
			{Name: "README.md"},
			{Name: "我的文档.txt"},
			{Name: "main.go"},
			{Name: ".git", IsDir: true},
		},
		key("src"): {
			{Name: "main.go"},
			{Name: "util.go"},
			{Name: "My File.go"},
			{Name: "sub", IsDir: true},
		},
		key("src", "sub"): {{Name: "deep.go"}},
		key("my project"): {{Name: "notes.txt"}},
		key(".git"):       {{Name: "config"}},
	}}
	return root, fs
}

func pathCompletionModel(on bool) *Model {
	root, fs := completionTestFS()
	val := "0"
	if on {
		val = "1"
	}
	env := func(k string) string {
		if k == "ARGENT_PATH_COMPLETION" {
			return val
		}
		return ""
	}
	return New(Config{Root: root, FS: fs, Env: env})
}

func candidateCommands(cands []CompletionCandidate) []string {
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.Command)
	}
	return out
}

func TestQuoteIfNeeded(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"src/main.go", "src/main.go"},
		{"我的文档.txt", "我的文档.txt"},
		{"src/My File.go", `"src/My File.go"`},
		{"我的 文档.txt", `"我的 文档.txt"`},
		{`"already quoted"`, `"already quoted"`},
		{`say "hi".txt`, `"say \"hi\".txt"`},
		{"a(b).go", `"a(b).go"`},
	} {
		if got := quoteIfNeeded(tc.in); got != tc.want {
			t.Errorf("quoteIfNeeded(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestActiveInlineWord(t *testing.T) {
	word, start, quoted := activeInlineWord("look src/ma", 11)
	if word != "src/ma" || start != 5 || quoted {
		t.Fatalf("plain token = (%q, %d, %v), want (src/ma, 5, false)", word, start, quoted)
	}

	// 引号内的路径可含空格；引号本身不属于词。
	quotedText := `"my project/no`
	word, start, quoted = activeInlineWord(quotedText, len([]rune(quotedText)))
	if word != "my project/no" || start != 1 || !quoted {
		t.Fatalf("quoted token = (%q, %d, %v), want (my project/no, 1, true)", word, start, quoted)
	}

	// 光标越界时钳制到文本末尾。
	word, start, quoted = activeInlineWord("abc", 999)
	if word != "abc" || start != 0 || quoted {
		t.Fatalf("clamped token = (%q, %d, %v), want (abc, 0, false)", word, start, quoted)
	}
}

func TestClassifyInlineWord(t *testing.T) {
	for _, tc := range []struct {
		word  string
		start int
		want  inlineCompletionKind
	}{
		{"@REA", 0, inlineAt},
		{"src/ma", 5, inlinePath},
		{"./x", 2, inlinePath},
		{"/co", 0, inlineNone}, // 行首 slash 命令归既有补全
		{"/compile src/ma", 9, inlinePath},
		{"hello", 0, inlineNone}, // 裸词不触发
		{"", 0, inlineNone},
	} {
		if got := classifyInlineWord(tc.word, tc.start); got != tc.want {
			t.Errorf("classifyInlineWord(%q, %d) = %v, want %v", tc.word, tc.start, got, tc.want)
		}
	}
}

func TestPathCompletionCandidates(t *testing.T) {
	root, fs := completionTestFS()

	cands := pathCompletionCandidates(fs, root, "src/ma")
	if got := candidateCommands(cands); !reflect.DeepEqual(got, []string{"src/main.go"}) {
		t.Fatalf("src/ma candidates = %v, want [src/main.go]", got)
	}

	// 目录列举：目录项带尾随 '/'，候选保留原始路径（引号推迟到接受时）。
	cands = pathCompletionCandidates(fs, root, "src/")
	got := candidateCommands(cands)
	sort.Strings(got)
	want := []string{"src/My File.go", "src/main.go", "src/sub/", "src/util.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("src/ candidates = %v, want %v", got, want)
	}

	// CJK 文件名与带空格的目录片段。
	if got := candidateCommands(pathCompletionCandidates(fs, root, "我的文")); !reflect.DeepEqual(got, []string{"我的文档.txt"}) {
		t.Fatalf("CJK candidates = %v, want [我的文档.txt]", got)
	}
	if got := candidateCommands(pathCompletionCandidates(fs, root, "my project/no")); !reflect.DeepEqual(got, []string{"my project/notes.txt"}) {
		t.Fatalf("spaced-dir candidates = %v, want [my project/notes.txt]", got)
	}
}

func TestAtCompletionCandidates(t *testing.T) {
	root, fs := completionTestFS()
	set := map[string]bool{}
	for _, c := range atCompletionCandidates(fs, root) {
		set[c.Command] = true
	}
	for _, want := range []string{
		"README.md", "我的文档.txt", "src/main.go", "src/sub/deep.go", "my project/notes.txt",
	} {
		if !set[want] {
			t.Errorf("@ candidates missing %q: %v", want, set)
		}
	}
	if set[".git/config"] {
		t.Error("@ candidates leaked ignored .git directory")
	}
}

func TestPathCandidatesFuzzySubsequence(t *testing.T) {
	root, fs := completionTestFS()
	s := NewCompletionState(atCompletionCandidates(fs, root))
	// 模糊子序列：非前缀缩写 "rdme" 命中 README.md，复用 matchCommand/FuzzyMatch。
	s.Query("rdme")
	if got := commandsOf(s); !reflect.DeepEqual(got, []string{"README.md"}) {
		t.Fatalf("fuzzy @ query visible = %v, want [README.md]", got)
	}
}

func TestPathCompletionModelAccept(t *testing.T) {
	m := pathCompletionModel(true)
	typeCommandText(m, "src/ma")
	if !m.completionActiveLocked() || m.completion == nil || !m.completion.inline {
		t.Fatal("path completion should be active for src/ma")
	}
	c, ok := m.completion.Current()
	if !ok || c.Command != "src/main.go" {
		t.Fatalf("current = (%+v, %v), want src/main.go", c, ok)
	}
	if action := m.HandleKey(Key{Kind: KindTab}); action != ActionNone {
		t.Fatalf("Tab returned %v, want ActionNone", action)
	}
	if got := m.Input(); got != "src/main.go " {
		t.Fatalf("input after accept = %q, want %q", got, "src/main.go ")
	}
	if m.TakeSubmitted() != "" {
		t.Fatal("accepting path completion submitted the turn")
	}
}

func TestAtMentionModelAccept(t *testing.T) {
	m := pathCompletionModel(true)
	typeCommandText(m, "@REA")
	c, ok := m.completion.Current()
	if !ok || c.Command != "README.md" {
		t.Fatalf("@ current = (%+v, %v), want README.md", c, ok)
	}
	m.HandleKey(Key{Kind: KindTab})
	if got := m.Input(); got != "@README.md " {
		t.Fatalf("input after @ accept = %q, want %q", got, "@README.md ")
	}
}

func TestPathCompletionQuoteWrapOnAccept(t *testing.T) {
	m := pathCompletionModel(true)
	typeCommandText(m, "src/M")
	c, ok := m.completion.Current()
	if !ok || c.Command != "src/My File.go" {
		t.Fatalf("current = (%+v, %v), want src/My File.go", c, ok)
	}
	m.HandleKey(Key{Kind: KindTab})
	// 路径含空格且不在引号内 → 写入时包裹双引号。
	if got, want := m.Input(), `"src/My File.go" `; got != want {
		t.Fatalf("quoted input = %q, want %q", got, want)
	}
}

func TestPathCompletionInsideQuotesNoDoubleQuote(t *testing.T) {
	m := pathCompletionModel(true)
	quotedText := `"my project/no`
	typeCommandText(m, quotedText)
	c, ok := m.completion.Current()
	if !ok || c.Command != "my project/notes.txt" {
		t.Fatalf("current = (%+v, %v), want my project/notes.txt", c, ok)
	}
	m.HandleKey(Key{Kind: KindTab})
	// 引号已在输入行中，接受时只补原名，不重复包裹。
	if got, want := m.Input(), `"my project/notes.txt `; got != want {
		t.Fatalf("quoted-context input = %q, want %q", got, want)
	}
}

func TestPathCompletionNoCandidateFallsBackToTab(t *testing.T) {
	m := pathCompletionModel(true)
	typeCommandText(m, "src/zzz")
	if m.completionActiveLocked() {
		t.Fatal("no-match path should not activate completion")
	}
	m.HandleKey(Key{Kind: KindTab})
	if got := m.Input(); got != "src/zzz  " {
		t.Fatalf("Tab fallback = %q, want two spaces", got)
	}
}

func TestPathCompletionOffKeepsSlashBaselineByteExact(t *testing.T) {
	root, fs := completionTestFS()
	// 门禁关闭（env=0）与默认开启（env=nil）的模型在 slash 命令输入上
	// 应逐字节一致：内联路径补全不接管 slash。
	on := New(Config{Root: root, FS: fs})
	off := pathCompletionModel(false)
	on.SetCommandInfo(commandInfos())
	off.SetCommandInfo(commandInfos())

	typeCommandText(on, "/co")
	typeCommandText(off, "/co")
	if on.Input() != off.Input() || on.completionActiveLocked() != off.completionActiveLocked() {
		t.Fatal("off baseline diverged on slash input state")
	}
	cp, _ := on.completion.Current()
	co, _ := off.completion.Current()
	if cp.Command != co.Command {
		t.Fatalf("off baseline candidate = %q, want %q", co.Command, cp.Command)
	}
	on.HandleKey(Key{Kind: KindTab})
	off.HandleKey(Key{Kind: KindTab})
	if got, want := frameBytes(off, 100, 24), frameBytes(on, 100, 24); got != want {
		t.Fatalf("off baseline frame bytes diverged:\ngot:\n%q\nwant:\n%q", got, want)
	}

	// 纯路径文本：关闭时不激活补全也不弹浮层；默认（nil env）则激活。
	off2 := pathCompletionModel(false)
	typeCommandText(off2, "src/ma")
	if off2.completionActiveLocked() || off2.completionOverlay {
		t.Fatal("off baseline must not complete path text")
	}
	on2 := New(Config{Root: root, FS: fs}) // env 为 nil → 默认开启
	typeCommandText(on2, "src/ma")
	if !on2.completionActiveLocked() {
		t.Fatal("default (nil env) must complete path text")
	}
}
