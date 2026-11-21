package i18n

import (
	"reflect"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestOptInMessageEveryKey 验证表中每个键在中英文下都返回对应文案。
func TestOptInMessageEveryKey(t *testing.T) {
	for key, entry := range optInMessageTable {
		t.Run(key, func(t *testing.T) {
			if got := OptInMessage(EnUS, key); got != entry.en {
				t.Errorf("OptInMessage(%q, %q) = %q, want %q", EnUS, key, got, entry.en)
			}
			if got := OptInMessage(ZhCN, key); got != entry.zh {
				t.Errorf("OptInMessage(%q, %q) = %q, want %q", ZhCN, key, got, entry.zh)
			}
		})
	}
}

// TestOptInMessageKnownKeys 锁定任务点名的 opt-in 与可靠性提示文案，防止表被意外改动。
func TestOptInMessageKnownKeys(t *testing.T) {
	cases := []struct {
		key string
		en  string
		zh  string
	}{
		{OptInEnabled, "opt-in %s enabled", "opt-in %s 已启用"},
		{OptInDisabled, "opt-in %s disabled", "opt-in %s 已停用"},
		{OptInUnknown, "opt-in status unknown for %s", "opt-in %s 的状态未知"},
		{ReliabilityGuidanceDedup, "consider deduplicating %d repeated tool calls", "建议对 %d 次重复的工具调用进行去重"},
		{ReliabilitySubstitutionDepth, "substitution depth limited to %d", "替换深度已限制为 %d"},
	}
	for _, c := range cases {
		if got := OptInMessage(EnUS, c.key); got != c.en {
			t.Errorf("OptInMessage(%q, %q) = %q, want %q", EnUS, c.key, got, c.en)
		}
		if got := OptInMessage(ZhCN, c.key); got != c.zh {
			t.Errorf("OptInMessage(%q, %q) = %q, want %q", ZhCN, c.key, got, c.zh)
		}
	}
}

// TestOptInMessageFormatting 验证在提供参数时按语言模板执行 fmt.Sprintf。
func TestOptInMessageFormatting(t *testing.T) {
	cases := []struct {
		name string
		lang string
		key  string
		args []any
		want string
	}{
		{"enabled en", EnUS, OptInEnabled, []any{"subagents"}, "opt-in subagents enabled"},
		{"enabled zh", ZhCN, OptInEnabled, []any{"subagents"}, "opt-in subagents 已启用"},
		{"disabled en", EnUS, OptInDisabled, []any{"hooks"}, "opt-in hooks disabled"},
		{"disabled zh", ZhCN, OptInDisabled, []any{"hooks"}, "opt-in hooks 已停用"},
		{"unknown en", EnUS, OptInUnknown, []any{"skills"}, "opt-in status unknown for skills"},
		{"unknown zh", ZhCN, OptInUnknown, []any{"skills"}, "opt-in skills 的状态未知"},
		{"failed en", EnUS, OptInActivationFailed, []any{"mcp", "timeout"}, "failed to activate opt-in mcp: timeout"},
		{"failed zh", ZhCN, OptInActivationFailed, []any{"mcp", "timeout"}, "激活 opt-in mcp 失败：timeout"},
		{"degraded zh", ZhCN, OptInDegraded, []any{"lsp"}, "opt-in lsp 处于降级模式"},
		{"dedup en", EnUS, ReliabilityGuidanceDedup, []any{3}, "consider deduplicating 3 repeated tool calls"},
		{"dedup zh", ZhCN, ReliabilityGuidanceDedup, []any{3}, "建议对 3 次重复的工具调用进行去重"},
		{"trim en", EnUS, ReliabilityGuidanceTrim, []any{512}, "consider trimming 512 characters of history"},
		{"trim zh", ZhCN, ReliabilityGuidanceTrim, []any{512}, "建议裁剪 512 个字符的历史记录"},
		{"checkpoint en", EnUS, ReliabilityGuidanceCheckpoint, nil, "consider saving a checkpoint before compaction"},
		{"checkpoint zh", ZhCN, ReliabilityGuidanceCheckpoint, nil, "建议在压缩前保存检查点"},
		{"depth en", EnUS, ReliabilitySubstitutionDepth, []any{4}, "substitution depth limited to 4"},
		{"depth zh", ZhCN, ReliabilitySubstitutionDepth, []any{4}, "替换深度已限制为 4"},
		{"fallback en", EnUS, ReliabilitySubstitutionFallback, []any{"old", "new"}, "substituted old with new"},
		{"fallback zh", ZhCN, ReliabilitySubstitutionFallback, []any{"old", "new"}, "已将 old 替换为 new"},
		{"limit zh", ZhCN, ReliabilitySubstitutionLimit, []any{8}, "替换次数已达上限 8"},
		{"no args en", EnUS, OptInEnabled, nil, "opt-in %s enabled"},
		{"no args zh", ZhCN, ReliabilitySubstitutionDepth, nil, "替换深度已限制为 %d"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := OptInMessage(c.lang, c.key, c.args...); got != c.want {
				t.Errorf("OptInMessage(%q, %q, %v) = %q, want %q", c.lang, c.key, c.args, got, c.want)
			}
		})
	}
}

// TestOptInMessageLocaleAliases 验证语言环境别名解析到正确的语言文案。
func TestOptInMessageLocaleAliases(t *testing.T) {
	zh := optInMessageTable[OptInEnabled].zh
	en := optInMessageTable[OptInEnabled].en

	for _, lang := range []string{"zh", "zh-cn", "zh_cn", "chinese", "中文", "ZH-CN", "Zh-Cn"} {
		if got := OptInMessage(lang, OptInEnabled); got != zh {
			t.Errorf("lang %q: got %q, want Chinese %q", lang, got, zh)
		}
	}
	for _, lang := range []string{"en", "en-us", "en_us", "english", "EN-US", "English"} {
		if got := OptInMessage(lang, OptInEnabled); got != en {
			t.Errorf("lang %q: got %q, want English %q", lang, got, en)
		}
	}
}

// TestOptInMessageUnknownLocaleFallsBackToEn 验证未知或空语言环境回退到英文。
func TestOptInMessageUnknownLocaleFallsBackToEn(t *testing.T) {
	want := "opt-in plugins enabled"
	for _, lang := range []string{"fr-FR", "ja-JP", "not-a-locale", ""} {
		if got := OptInMessage(lang, OptInEnabled, "plugins"); got != want {
			t.Errorf("lang %q: got %q, want fallback %q", lang, got, want)
		}
	}
}

// TestOptInMessageMissingKey 验证未知键返回带键的稳定占位串且不 panic。
func TestOptInMessageMissingKey(t *testing.T) {
	for _, lang := range []string{EnUS, ZhCN, "fr-FR", "中文", ""} {
		const key = "optin.brand_new"
		first := OptInMessage(lang, key, "ignored-arg")
		second := OptInMessage(lang, key)
		if first == "" || first != second {
			t.Errorf("lang %q: unstable placeholder: %q vs %q", lang, first, second)
		}
		if !strings.Contains(first, key) {
			t.Errorf("lang %q: placeholder %q does not contain key %q", lang, first, key)
		}
	}
}

// TestOptInMessageKeysSortedAndStable 验证键列表稳定排序、覆盖全部键且返回独立切片。
func TestOptInMessageKeysSortedAndStable(t *testing.T) {
	got := OptInMessageKeys()
	again := OptInMessageKeys()
	if !reflect.DeepEqual(got, again) {
		t.Fatalf("OptInMessageKeys not stable: %v vs %v", got, again)
	}
	if !sort.StringsAreSorted(got) {
		t.Errorf("OptInMessageKeys not sorted: %v", got)
	}

	want := make([]string, 0, len(optInMessageTable))
	for key := range optInMessageTable {
		want = append(want, key)
	}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("OptInMessageKeys = %v, want %v", got, want)
	}

	// 修改返回的切片不应影响表或后续调用。
	if len(got) > 0 {
		got[0] = "corrupted"
	}
	if !reflect.DeepEqual(OptInMessageKeys(), want) {
		t.Errorf("mutating returned keys affected later calls: %v", OptInMessageKeys())
	}
}

// TestOptInMessageCJK 验证含 CJK 的文案与键保持完整 UTF-8 字节序列。
func TestOptInMessageCJK(t *testing.T) {
	for _, key := range OptInMessageKeys() {
		zh := OptInMessage(ZhCN, key)
		if !utf8.ValidString(zh) || !containsCJK(zh) {
			t.Errorf("key %q: Chinese message not valid CJK: %q", key, zh)
		}
	}

	// 未知 CJK 键应原样出现在占位串中，不丢失或截断任何字节。
	const cjkKey = "optin.配额不足：链接已失效 🪙"
	for _, lang := range []string{EnUS, ZhCN, "中文"} {
		got := OptInMessage(lang, cjkKey)
		if !utf8.ValidString(got) || !strings.Contains(got, cjkKey) {
			t.Errorf("lang %q: CJK key corrupted in placeholder: %q", lang, got)
		}
	}
}

// TestOptInMessageDoesNotMutateArgs 验证调用不修改传入的参数切片或其指向的数据。
func TestOptInMessageDoesNotMutateArgs(t *testing.T) {
	args := []any{"mcp", 7}
	original := append([]any(nil), args...)
	_ = OptInMessage(ZhCN, OptInActivationFailed, args...)
	if !reflect.DeepEqual(args, original) {
		t.Errorf("args slice mutated: got %v, want %v", args, original)
	}

	data := []int{1, 2, 3}
	snapshot := []int{1, 2, 3}
	_ = OptInMessage(EnUS, ReliabilitySubstitutionFallback, &data, "new")
	if !reflect.DeepEqual(data, snapshot) {
		t.Errorf("pointed-to data mutated: got %v, want %v", data, snapshot)
	}
}

// TestOptInMessagePure 验证 OptInMessage 不向 Missing 记录任何内容。
func TestOptInMessagePure(t *testing.T) {
	ResetMissing()
	_ = OptInMessage(ZhCN, OptInEnabled, "hooks")
	_ = OptInMessage(EnUS, "optin.brand_new")
	if got := Missing(); len(got) != 0 {
		t.Errorf("OptInMessage recorded missing keys: %v", got)
	}
}
