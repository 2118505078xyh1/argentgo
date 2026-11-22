package tui

import (
	"reflect"
	"strings"
	"testing"
)

// aliasTestEnv 返回一个只注入别名配置的 env 查找，其余变量一律返回空串，
// 以免触发 NO_COLOR / TERM 等无关的降级路径。
func aliasTestEnv(spec string) func(string) string {
	return func(key string) string {
		if key == keyAliasSpecEnv {
			return spec
		}
		return ""
	}
}

// TestResolveKeyActionWrapper 验证导出包装复用 resolveBinding 的全部语义：
// 基本命中、大小写折叠与未命中。
func TestResolveKeyActionWrapper(t *testing.T) {
	aliases := map[string][]string{"scroll_up": {"k"}}
	cases := []struct {
		key    string
		want   string
		wantOK bool
	}{
		{"k", "scroll_up", true},
		{"K", "scroll_up", true}, // 大小写折叠
		{"x", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		action, ok := ResolveKeyAction(tc.key, aliases)
		if ok != tc.wantOK || action != tc.want {
			t.Errorf("ResolveKeyAction(%q) = (%q, %v), want (%q, %v)", tc.key, action, ok, tc.want, tc.wantOK)
		}
	}
}

// TestParseAndBuildKeyAliases 验证默认表 + 配置块 + env 串的分层合并，
// 以及大小写折叠在合并后的别名表上依然生效。
func TestParseAndBuildKeyAliases(t *testing.T) {
	spec := "scroll_up=k,K,上;scroll_down=j"
	block := map[string]any{
		"key_aliases": map[string]any{
			"submit": []string{"s", "S"},
		},
	}
	merged := BuildKeyAliases(block, spec)

	for key, want := range map[string]string{
		"k": "scroll_up", "K": "scroll_up", "上": "scroll_up",
		"j": "scroll_down",
		"s": "submit", "S": "submit",
	} {
		if action, ok := ResolveKeyAction(key, merged); !ok || action != want {
			t.Errorf("ResolveKeyAction(%q) = (%q, %v), want (%q, true)", key, action, ok, want)
		}
	}
	if action, ok := ResolveKeyAction("x", merged); ok || action != "" {
		t.Errorf("unconfigured key must miss: (%q, %v)", action, ok)
	}

	// 合并不得修改默认表或配置块入参。
	fresh := DefaultKeyAliases()
	if got, want := len(fresh), len(DefaultKeyAliases()); got != want {
		t.Fatalf("default table grew across calls: %d vs %d", got, want)
	}
	if !reflect.DeepEqual(block, map[string]any{
		"key_aliases": map[string]any{"submit": []string{"s", "S"}},
	}) {
		t.Fatal("BuildKeyAliases mutated the config block")
	}
}

// aliasScrollFixture 构造一份可滚动的模型，并返回首帧前的 scroll 基线。
func aliasScrollFixture(t *testing.T, spec string) *Model {
	t.Helper()
	m := New(Config{Env: aliasTestEnv(spec)})
	for i := 0; i < 50; i++ {
		m.AppendNotice("line")
	}
	m.Frame(80, 24)
	if m.scroll != 0 {
		t.Fatalf("fixture scroll = %d, want 0", m.scroll)
	}
	return m
}

// TestKeyAliasScrollViaHandleKey 覆盖任务主路径：resolveBinding("k",aliases)
// 得到 scroll_up，且经 handleKeyLocked（经 HandleKey）真正触发滚动，
// 同时该字符不再被插入输入行。
func TestKeyAliasScrollViaHandleKey(t *testing.T) {
	aliases := map[string][]string{"scroll_up": {"k"}}
	if action, ok := resolveBinding("k", aliases); !ok || action != "scroll_up" {
		t.Fatalf("resolveBinding(k) = (%q, %v), want (scroll_up, true)", action, ok)
	}

	m := aliasScrollFixture(t, "scroll_up=k")
	if action := m.HandleKey(Key{Kind: KindRune, Rune: 'k'}); action != ActionNone {
		t.Fatalf("alias key action = %v, want ActionNone", action)
	}
	if m.scroll <= 0 {
		t.Fatalf("alias key did not scroll transcript, scroll = %d", m.scroll)
	}
	if got := m.Input(); got != "" {
		t.Fatalf("alias key leaked into input: %q", got)
	}
}

// TestKeyAliasConfigUppercaseK 验证配置别名后大写 K 经大小写折叠同样生效。
func TestKeyAliasConfigUppercaseK(t *testing.T) {
	m := aliasScrollFixture(t, "scroll_up=k")
	m.HandleKey(Key{Kind: KindRune, Rune: 'K'})
	if m.scroll <= 0 {
		t.Fatalf("uppercase K did not scroll, scroll = %d", m.scroll)
	}
}

// TestKeyAliasCJK 验证 CJK 单字别名经 handleKeyLocked 触发滚动。
func TestKeyAliasCJK(t *testing.T) {
	m := aliasScrollFixture(t, "scroll_up=上")
	m.HandleKey(Key{Kind: KindRune, Rune: '上'})
	if m.scroll <= 0 {
		t.Fatalf("CJK alias did not scroll, scroll = %d", m.scroll)
	}
}

// TestKeyAliasLeavesBoundKeysAndPlainRunesAlone 验证"已绑定键优先、
// 别名只补充"：专属 Kind 仍走主 switch，未配置的可打印键仍正常插入。
func TestKeyAliasLeavesBoundKeysAndPlainRunesAlone(t *testing.T) {
	m := New(Config{Env: aliasTestEnv("scroll_up=k")})
	m.HandleKey(Key{Kind: KindRune, Rune: 'x'})
	if got := m.Input(); got != "x" {
		t.Fatalf("unconfigured rune should insert, got %q", got)
	}

	for i := 0; i < 50; i++ {
		m.AppendNotice("line")
	}
	m.Frame(80, 24)
	m.HandleKey(Key{Kind: KindPageUp})
	if m.scroll <= 0 {
		t.Fatalf("dedicated page-up key should still scroll, scroll = %d", m.scroll)
	}
}

// aliasFrameBytes 把一帧渲染为稳定字符串，便于逐字节比较。
func aliasFrameBytes(m *Model, w, h int) string {
	buf := m.Frame(w, h)
	var sb strings.Builder
	for y := 0; y < buf.Height(); y++ {
		sb.WriteString(buf.RowText(y))
		sb.WriteByte('\n')
	}
	return sb.String()
}

// TestNoAliasConfigFrameByteIdentical 验证无配置时旁路完全不介入：
// 注入一个始终返回空串的 env（等价于未配置）与完全无 env 的模型，
// 在相同内容下渲染逐字节一致；同一模型连续两帧也一致。
func TestNoAliasConfigFrameByteIdentical(t *testing.T) {
	noEnv := New(Config{})
	emptyEnv := New(Config{Env: aliasTestEnv("")})
	for i := 0; i < 30; i++ {
		noEnv.AppendNotice("line")
		emptyEnv.AppendNotice("line")
	}
	first := aliasFrameBytes(noEnv, 80, 24)
	if got := aliasFrameBytes(emptyEnv, 80, 24); got != first {
		t.Fatalf("empty alias env changed frame bytes:\nno-env  =%q\nemptyEnv=%q", first, got)
	}
	if got := aliasFrameBytes(noEnv, 80, 24); got != first {
		t.Fatal("repeated frame is not byte-identical")
	}
}
