package tui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// 本文件只增强 TUI 交互层的不变量守护，不改生产逻辑。所有断言都针对
// streambuf.go / approval.go / theme.go / keymap.go / screen.go 已公开的
// （或同包可见的）纯函数契约。复用同包既有辅助 waitFor（见 model_test.go）；
// 本文件新增的辅助一律以 guard 前缀命名，避免与其它守护文件冲突。
// 刻意不引用 completion_test.go —— 它由另一个代理并行编写。

// guardCommitFrontierCuts 对单个样本做 frontier 守护。
// 切点取所有 rune 边界（ASCII 标记的每个字节都是 rune 边界，因此
// `/`、单反引号、三反引号围栏、表格的每一处字节切点都被覆盖；
// CJK/emoji 则按完整字素切分，不制造非法 UTF-8 的半个 rune）：
// ① 各切点的 commit 位置单调不减（提交过的字节绝不会被收回，
//
//	这是“不闪块”的核心）；② 切割只落在 rune 边界（不拆 UTF-8）；
//
// ③ 结果对同一输入确定；④ 逐 rune 喂入与两段推送 + Flush 无损且保序。
func guardCommitFrontierCuts(t *testing.T, sample string) {
	t.Helper()

	// 一次性推入的基线：所有字节最终按顺序输出。
	var all MarkdownStreamBuf
	if got := all.Push(sample) + all.Flush(); got != sample {
		t.Fatalf("one-shot emission = %q, want %q", got, sample)
	}

	// 收集所有 rune 边界作为合法切点。
	cuts := make([]int, 0, len(sample)+1)
	for i := range sample {
		if i > 0 {
			cuts = append(cuts, i)
		}
	}
	cuts = append(cuts, len(sample))

	prev := 0
	for _, k := range cuts[1:] {
		prefix := sample[:k]
		cut := commitFrontier(prefix)
		if cut < prev {
			t.Fatalf("frontier rewound at cut %d: cut=%d < prev=%d (sample %q)", k, cut, prev, sample)
		}
		if cut > k || cut < 0 {
			t.Fatalf("frontier out of range at cut %d: cut=%d", k, cut)
		}
		if cut > prev && !utf8.ValidString(sample[prev:cut]) {
			t.Fatalf("frontier cut at %d splits a UTF-8 rune: %q", k, sample[prev:cut])
		}
		// 确定性：同一前缀重复求值结果相同。
		if again := commitFrontier(prefix); again != cut {
			t.Fatalf("commitFrontier(%q) unstable: %d then %d", prefix, cut, again)
		}
		prev = cut
	}

	// 逐 rune 喂入：累积输出必须始终是样本的一个前缀，且最终无损。
	var bytewise MarkdownStreamBuf
	var acc strings.Builder
	for i := 0; i < len(sample); {
		_, size := utf8.DecodeRuneInString(sample[i:])
		acc.WriteString(bytewise.Push(sample[i : i+size]))
		if emitted := acc.String(); !strings.HasPrefix(sample, emitted) {
			t.Fatalf("rune-wise emission %q is not a prefix of %q", emitted, sample)
		}
		i += size
	}
	acc.WriteString(bytewise.Flush())
	if got := acc.String(); got != sample {
		t.Fatalf("rune-wise emission = %q, want %q", got, sample)
	}

	// 枚举每个 rune 切点：两段推送的输出必须保序且无损。
	for _, k := range cuts {
		var b MarkdownStreamBuf
		first := b.Push(sample[:k])
		if !strings.HasPrefix(sample, first) {
			t.Fatalf("cut %d: first emission %q is not a prefix of %q", k, first, sample)
		}
		second := b.Push(sample[k:])
		got := first + second + b.Flush()
		if got != sample {
			t.Fatalf("cut %d: emission = %q, want %q", k, got, sample)
		}
	}
}

// TestGuardStreambufFrontierStableAtEveryByteCut 守护流式 markdown 在
// 任意 rune/标记边界切断（含 `/`、单反引号、三反引号围栏、表格、
// CJK/emoji）时的 frontier 稳定性与无损性。
func TestGuardStreambufFrontierStableAtEveryByteCut(t *testing.T) {
	samples := []string{
		"plain text without markers",
		"a / slash and /`tick`/ and ```fence``` mid",
		"plain **bold** and `inline code` text",
		"line\n```go\nfmt.Println(`hi`)\n```\ntail",
		"中文🙂 **粗体** 与 `代码` 混排",
		"a|b\n---|---\n1|2\noutro\n",
		"* * _ __ ** ``` `~ /", // 标记密集样本
	}
	for _, sample := range samples {
		guardCommitFrontierCuts(t, sample)
	}
}

// guardLinesFingerprint 把渲染行序列化成包含样式与顺序的字节串，
// 用于逐字节比较两个弹窗是否一致（文本相同但样式不同也必须被抓到）。
func guardLinesFingerprint(lines []Line) string {
	var b strings.Builder
	for _, line := range lines {
		for _, span := range line.Spans {
			b.WriteString(span.Style.SGR())
			b.WriteByte('[')
			b.WriteString(span.Text)
			b.WriteByte(']')
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// guardOpenApproval 通过给定的入口点唤起弹窗，返回作答通道。
// danger=true 时走 ApproveDanger 并传空原因，用来与 Approve 对照。
func guardOpenApproval(t *testing.T, m *Model, useDanger bool, name, args string) <-chan bool {
	t.Helper()
	done := make(chan bool, 1)
	go func() {
		var ok bool
		var err error
		if useDanger {
			ok, err = m.ApproveDanger(context.Background(), name, args, "")
		} else {
			ok, err = m.Approve(context.Background(), name, args)
		}
		if err != nil {
			t.Errorf("guardOpenApproval: %v", err)
		}
		done <- ok
	}()
	waitFor(t, "the approval modal", func() bool {
		_, _, ok := m.PendingApproval()
		return ok
	})
	return done
}

// guardResolveApproval 向弹窗发送一个按键并等待作答结果。
func guardResolveApproval(t *testing.T, m *Model, done <-chan bool, key Key) bool {
	t.Helper()
	m.HandleKey(key)
	select {
	case ok := <-done:
		return ok
	case <-time.After(2 * time.Second):
		t.Fatal("approval did not resolve")
		return false
	}
}

// TestGuardApproveDangerEmptyMatchesApproveByteForByte 证明 danger 为空时
// ApproveDanger 与 Approve 的弹窗渲染逐字节相同，且不翻转默认答案。
func TestGuardApproveDangerEmptyMatchesApproveByteForByte(t *testing.T) {
	const (
		width = 60
		name  = "bash"
		args  = `{"command":"ls -la"}`
	)
	plain := New(Config{Locale: "en"})
	danger := New(Config{Locale: "en"})
	plainDone := guardOpenApproval(t, plain, false, name, args)
	dangerDone := guardOpenApproval(t, danger, true, name, args)

	if got := guardLinesFingerprint(plain.approvalLines(width)); got != guardLinesFingerprint(danger.approvalLines(width)) {
		t.Fatalf("danger=\"\" modal differs from Approve:\nplain:\n%s\ndanger:\n%s",
			guardLinesFingerprint(plain.approvalLines(width)), guardLinesFingerprint(danger.approvalLines(width)))
	}
	if danger.pending.defaultsToDeny() {
		t.Fatal("empty danger must not default to Deny")
	}

	// 非危险入口预选 Allow，Enter 确认默认项即放行；空 danger 与 Approve 一致。
	if ok := guardResolveApproval(t, plain, plainDone, Key{Kind: KindEnter}); !ok {
		t.Fatal("Enter must approve the plain modal by default")
	}
	if ok := guardResolveApproval(t, danger, dangerDone, Key{Kind: KindRune, Rune: 'n'}); ok {
		t.Fatal("n must deny the empty-danger modal")
	}
}

// TestGuardGrantKeyNULSeparation 守护会话级授权 key 的 NUL 分隔：
// 朴素拼接会撞车的 name/detail 组合在这里必须互不冲突。
func TestGuardGrantKeyNULSeparation(t *testing.T) {
	// "a"+"b" 与 "ab"+"" 朴素拼接都是 "ab"，NUL 必须把它们分开。
	a := approvalGrantKey("a", "b")
	b := approvalGrantKey("ab", "")
	if "a"+"b" != "ab"+"" {
		t.Fatal("sanity: naive concatenation should collide")
	}
	if a == b {
		t.Fatalf("grant keys must not collide: %q vs %q", a, b)
	}
	if got := strings.Count(a, "\x00"); got != 1 {
		t.Fatalf("grant key %q has %d NUL separators, want 1", a, got)
	}

	// detail 提取与 key 构造确定，重复调用结果一致。
	const want = "bash\x00sudo rm"
	for i := 0; i < 8; i++ {
		if got := approvalGrantKey("bash", `{"command":"sudo rm"}`); got != want {
			t.Fatalf("approvalGrantKey = %q, want %q", got, want)
		}
	}

	// 集成：按 'a' 授权的精确 key 被写入，参数不同的调用不能蹭到授权。
	m := New(Config{Locale: "en"})
	done := guardOpenApproval(t, m, false, "bash", `{"command":"ls"}`)
	if ok := guardResolveApproval(t, m, done, Key{Kind: KindRune, Rune: 'a'}); !ok {
		t.Fatal("a should allow the first command")
	}
	granted := approvalGrantKey("bash", `{"command":"ls"}`)
	other := approvalGrantKey("bash", `{"command":"ls -la"}`)
	if granted == other {
		t.Fatal("different arguments must map to different grant keys")
	}
	m.mu.Lock()
	_, hasGranted := m.allowed[granted]
	_, hasOther := m.allowed[other]
	m.mu.Unlock()
	if !hasGranted {
		t.Fatalf("session grant key %q not recorded", granted)
	}
	if hasOther {
		t.Fatalf("session grant leaked to %q", other)
	}
}

// guardPaletteZero 报告调色板的所有语义颜色是否都被清空（单色）。
func guardPaletteZero(p Palette) bool {
	for _, c := range []Color{
		p.User, p.Assistant, p.Tool, p.Reasoning, p.Error, p.Accent,
		p.Border, p.Muted, p.Warning, p.Code,
	} {
		if c != (Color{}) {
			return false
		}
	}
	return true
}

// guardEnvLookup 从键值对序列构造一个注入式环境查找函数；未设置的键返回空串。
func guardEnvLookup(kv ...string) func(string) string {
	return func(key string) string {
		for i := 0; i+1 < len(kv); i += 2 {
			if kv[i] == key {
				return kv[i+1]
			}
		}
		return ""
	}
}

// TestGuardThemeNamesAndAliasesAreStable 守护主题名与别名的稳定性、
// 确定性，以及 ThemeNames 返回全新切片（改返回值不影响后续调用）。
func TestGuardThemeNamesAndAliasesAreStable(t *testing.T) {
	want := []string{"dark", "light", "ansi"}
	if got := ThemeNames(); !reflect.DeepEqual(got, want) {
		t.Fatalf("ThemeNames() = %v, want %v", got, want)
	}
	if ThemeDark != "dark" || ThemeLight != "light" || ThemeANSI != "ansi" {
		t.Fatalf("theme constants drifted: %q %q %q", ThemeDark, ThemeLight, ThemeANSI)
	}

	names := ThemeNames()
	names[0] = "mutated"
	if got := ThemeNames(); !reflect.DeepEqual(got, want) {
		t.Fatalf("ThemeNames() shares backing state: %v", got)
	}

	cases := map[string]string{
		"": ThemeDark, "  ": ThemeDark, "DARK": ThemeDark, " dark ": ThemeDark,
		"Light": ThemeLight, "ANSI": ThemeANSI, "16": ThemeANSI,
		"16color": ThemeANSI, "16-COLOR": ThemeANSI, " neon ": "neon",
	}
	for in, wantCanonical := range cases {
		if got := canonicalTheme(in); got != wantCanonical {
			t.Errorf("canonicalTheme(%q) = %q, want %q", in, got, wantCanonical)
		}
	}
	for _, name := range want {
		if _, err := paletteFor(name); err != nil {
			t.Errorf("paletteFor(%q): %v", name, err)
		}
	}
	if _, err := paletteFor("neon"); !errors.Is(err, ErrUnknownTheme) {
		t.Errorf("paletteFor(neon) error = %v, want ErrUnknownTheme", err)
	}

	first, _ := paletteFor(ThemeANSI)
	for i := 0; i < 8; i++ {
		got, err := paletteFor(ThemeANSI)
		if err != nil || !reflect.DeepEqual(got, first) {
			t.Fatalf("paletteFor(ansi) nondeterministic at %d: %+v, %v", i, got, err)
		}
	}
}

// TestGuardDegradationInjectedEnvAndMonochrome 守护 NO_COLOR / dumb 的
// 纯函数降级判定，以及降级后调色板单色但保留粗体/暗色属性。
func TestGuardDegradationInjectedEnvAndMonochrome(t *testing.T) {
	cases := []struct {
		name   string
		lookup func(string) string
		want   bool
	}{
		{"nil 查找", nil, false},
		{"空环境", guardEnvLookup(), false},
		{"NO_COLOR=1", guardEnvLookup("NO_COLOR", "1", "TERM", "xterm"), true},
		{"NO_COLOR 仅空白", guardEnvLookup("NO_COLOR", "  ", "TERM", "xterm"), false},
		{"TERM=dumb", guardEnvLookup("TERM", "dumb"), true},
		{"TERM=DUMB 大小写与空白", guardEnvLookup("TERM", " DuMb "), true},
		{"普通终端", guardEnvLookup("TERM", "xterm-256color"), false},
	}
	for _, tc := range cases {
		if got := detectDegradation(tc.lookup); got != tc.want {
			t.Errorf("detectDegradation(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}

	// monochrome 清空所有颜色，但保留 Bold/Dim 属性与 noColor 标志。
	base, _ := paletteFor(ThemeDark)
	base.noColor = false
	mono := monochrome(base)
	if !guardPaletteZero(mono) {
		t.Fatal("monochrome left semantic colours behind")
	}
	if !mono.user().Bold || !mono.accent().Bold || !mono.dim().Dim {
		t.Fatal("monochrome dropped bold/dim attributes")
	}
	if mono.user().Fg != (Color{}) {
		t.Fatal("monochrome user style still has a foreground colour")
	}

	// 集成：注入环境触发降级，模型调色板随之变为单色。
	for _, tc := range []struct {
		name string
		env  func(string) string
	}{
		{"NO_COLOR", guardEnvLookup("NO_COLOR", "1")},
		{"dumb", guardEnvLookup("TERM", "dumb")},
	} {
		m := New(Config{Locale: "en", Env: tc.env})
		if !m.Degraded() {
			t.Errorf("%s: model not degraded", tc.name)
		}
		if !guardPaletteZero(m.Palette()) {
			t.Errorf("%s: degraded model palette still has colours", tc.name)
		}
	}

	// 显式 NoColor：不标记降级，但调色板同样单色。
	explicit := New(Config{Locale: "en", NoColor: true})
	if explicit.Degraded() || !explicit.NoColor() || !guardPaletteZero(explicit.Palette()) {
		t.Fatal("explicit NoColor must suppress colours without marking env degradation")
	}
}

// guardCloneAliases 深拷贝别名表，用于检测入参是否被修改。
func guardCloneAliases(src map[string][]string) map[string][]string {
	out := make(map[string][]string, len(src))
	for k, v := range src {
		out[k] = append([]string(nil), v...)
	}
	return out
}

// TestGuardKeymapMultiAliasConsistencyAndPurity 守护多别名一致性：
// 同一动作的别名都解析到该动作，大小写折叠与 CJK/emoji 别名可用，
// 结果确定且不修改入参；mergeAliases 不修改 src，且结果与遍历顺序无关。
func TestGuardKeymapMultiAliasConsistencyAndPurity(t *testing.T) {
	aliases := map[string][]string{
		"down":   {"down", "ctrl+j", "j", "下"},
		"copy":   {"c", "y", "复制", "📋"},
		"submit": {"enter", "↵"},
	}
	snapshot := guardCloneAliases(aliases)
	lookup := map[string]string{
		"down": "down", "Ctrl+J": "down", "J": "down", "下": "down",
		"c": "copy", "Y": "copy", "复制": "copy", "📋": "copy",
		"ENTER": "submit", "↵": "submit",
	}
	for i := 0; i < 32; i++ {
		for key, want := range lookup {
			action, ok := resolveBinding(key, aliases)
			if !ok || action != want {
				t.Fatalf("iteration %d: resolveBinding(%q) = (%q, %v), want (%q, true)", i, key, action, ok, want)
			}
		}
	}
	if !reflect.DeepEqual(aliases, snapshot) {
		t.Fatal("resolveBinding mutated its input")
	}
	if action, ok := resolveBinding("不存在", aliases); ok || action != "" {
		t.Fatalf("unrelated CJK key must miss: (%q, %v)", action, ok)
	}

	src := guardCloneAliases(aliases)
	dst := map[string][]string{"down": {"j"}}
	mergeAliases(dst, src)
	if !reflect.DeepEqual(src, snapshot) {
		t.Fatal("mergeAliases mutated src")
	}
	first := guardCloneAliases(dst)
	for i := 0; i < 16; i++ {
		fresh := map[string][]string{"down": {"j"}}
		mergeAliases(fresh, src)
		if !reflect.DeepEqual(fresh, first) {
			t.Fatalf("mergeAliases nondeterministic at %d: %v vs %v", i, fresh, first)
		}
	}
}

// TestGuardTextWidthGuardsCJKAndEmojiCandidates 用 TextWidth 守护
// CJK/emoji 候选的显示宽度：ZWJ 家族与旗帜各算两格，组合符算零宽，
// 且 ShortHelp 在窄宽度下既不超宽也不切坏 UTF-8。
func TestGuardTextWidthGuardsCJKAndEmojiCandidates(t *testing.T) {
	cases := []struct {
		text string
		want int
	}{
		{"", 0},
		{"abc", 3},
		{"中文", 4},
		{"复制", 4},
		{"🙂", 2},
		{"📋", 2},
		{"👨‍👩‍👧‍👦", 2}, // ZWJ 家庭：整簇两格
		{"🇨🇳", 2},      // 区域指示符对：旗帜两格
		{"e\u0301", 1}, // 组合重音符：零宽，整体一格
		{"a🙂中", 5},     // 1 + 2 + 2
	}
	for _, tc := range cases {
		if got := TextWidth(tc.text); got != tc.want {
			t.Errorf("TextWidth(%q) = %d, want %d", tc.text, got, tc.want)
		}
	}

	bindings := []KeyBinding{
		{Key: "复制", Desc: "拷贝选中", Enabled: true},
		{Key: "📋", Desc: "粘贴", Enabled: true},
	}
	for _, width := range []int{0, 1, 6, 12, 24, 80} {
		out := ShortHelp(bindings, width, TextWidth)
		if !utf8.ValidString(out) {
			t.Fatalf("ShortHelp(width=%d) emitted invalid UTF-8: %q", width, out)
		}
		if got := TextWidth(out); got > width {
			t.Fatalf("ShortHelp(width=%d) = %q spans %d cells", width, out, got)
		}
	}
}
