package i18n

import (
	"reflect"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestReliabilityMessageEveryKey 验证表中每个键在中英文下都返回对应文案。
func TestReliabilityMessageEveryKey(t *testing.T) {
	for key, entry := range reliabilityMessageTable {
		t.Run(key, func(t *testing.T) {
			if got := ReliabilityMessage(EnUS, key); got != entry.en {
				t.Errorf("ReliabilityMessage(%q, %q) = %q, want %q", EnUS, key, got, entry.en)
			}
			if got := ReliabilityMessage(ZhCN, key); got != entry.zh {
				t.Errorf("ReliabilityMessage(%q, %q) = %q, want %q", ZhCN, key, got, entry.zh)
			}
		})
	}
}

// TestReliabilityMessageKnownKeys 锁定任务点名的可靠性消息文案，防止表被意外改动。
func TestReliabilityMessageKnownKeys(t *testing.T) {
	cases := []struct {
		key string
		en  string
		zh  string
	}{
		{ReliabilityCompactionLossy, "compaction discarded %d messages; details may be lost", "压缩已丢弃 %d 条消息，部分细节可能丢失"},
		{ReliabilityFocusWandering, "topic drifting: %d steps since last user instruction", "主题漂移：距上次用户指令已 %d 步"},
		{ReliabilityApprovalDenied, "approval denied for %s", "操作 %s 的审批被拒绝"},
		{ReliabilityOutputClipped, "output clipped to %d bytes", "输出已裁剪至 %d 字节"},
	}
	for _, c := range cases {
		if got := ReliabilityMessage(EnUS, c.key); got != c.en {
			t.Errorf("ReliabilityMessage(%q, %q) = %q, want %q", EnUS, c.key, got, c.en)
		}
		if got := ReliabilityMessage(ZhCN, c.key); got != c.zh {
			t.Errorf("ReliabilityMessage(%q, %q) = %q, want %q", ZhCN, c.key, got, c.zh)
		}
	}
}

// TestReliabilityMessageFormatting 验证在提供参数时按语言模板执行 fmt.Sprintf。
func TestReliabilityMessageFormatting(t *testing.T) {
	cases := []struct {
		name string
		lang string
		key  string
		args []any
		want string
	}{
		{"lossy en", EnUS, ReliabilityCompactionLossy, []any{3}, "compaction discarded 3 messages; details may be lost"},
		{"lossy zh", ZhCN, ReliabilityCompactionLossy, []any{3}, "压缩已丢弃 3 条消息，部分细节可能丢失"},
		{"impact en", EnUS, ReliabilityCompactionImpact, []any{9000, 4000}, "compaction reduced context from 9000 to 4000 tokens"},
		{"impact zh", ZhCN, ReliabilityCompactionImpact, []any{9000, 4000}, "压缩将上下文从 9000 个 token 降至 4000"},
		{"wandering en", EnUS, ReliabilityFocusWandering, []any{5}, "topic drifting: 5 steps since last user instruction"},
		{"wandering zh", ZhCN, ReliabilityFocusWandering, []any{5}, "主题漂移：距上次用户指令已 5 步"},
		{"pressure zh", ZhCN, ReliabilityFocusPressure, []any{85}, "上下文压力偏高：已使用预算的 85%"},
		{"denied en", EnUS, ReliabilityApprovalDenied, []any{"rm -rf /tmp"}, "approval denied for rm -rf /tmp"},
		{"denied zh", ZhCN, ReliabilityApprovalDenied, []any{"rm -rf /tmp"}, "操作 rm -rf /tmp 的审批被拒绝"},
		{"pending en", EnUS, ReliabilityApprovalPending, []any{"fs.write"}, "waiting for approval: fs.write"},
		{"audit zh", ZhCN, ReliabilityApprovalAudit, []any{"shell.exec"}, "已为 shell.exec 记录审计"},
		{"clipped en", EnUS, ReliabilityOutputClipped, []any{1024}, "output clipped to 1024 bytes"},
		{"clipped zh", ZhCN, ReliabilityOutputClipped, []any{1024}, "输出已裁剪至 1024 字节"},
		{"truncated zh", ZhCN, ReliabilityOutputTruncated, []any{"tail"}, "输出已截断：tail"},
		{"dedupe en", EnUS, ReliabilityDedupeSuppressed, []any{2}, "suppressed 2 duplicate tool calls"},
		{"steering zh", ZhCN, ReliabilitySteeringDiverted, []any{4}, "转向已改道 4 个进行中的步骤"},
		{"no args en", EnUS, ReliabilityApprovalDenied, nil, "approval denied for %s"},
		{"no args zh", ZhCN, ReliabilityOutputClipped, nil, "输出已裁剪至 %d 字节"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ReliabilityMessage(c.lang, c.key, c.args...); got != c.want {
				t.Errorf("ReliabilityMessage(%q, %q, %v) = %q, want %q", c.lang, c.key, c.args, got, c.want)
			}
		})
	}
}

// TestReliabilityMessageLocaleAliases 验证语言环境别名解析到正确的语言文案。
func TestReliabilityMessageLocaleAliases(t *testing.T) {
	zh := reliabilityMessageTable[ReliabilityCompactionLossy].zh
	en := reliabilityMessageTable[ReliabilityCompactionLossy].en

	for _, lang := range []string{"zh", "zh-cn", "zh_cn", "chinese", "中文", "ZH-CN", "Zh-Cn"} {
		if got := ReliabilityMessage(lang, ReliabilityCompactionLossy); got != zh {
			t.Errorf("lang %q: got %q, want Chinese %q", lang, got, zh)
		}
	}
	for _, lang := range []string{"en", "en-us", "en_us", "english", "EN-US", "English"} {
		if got := ReliabilityMessage(lang, ReliabilityCompactionLossy); got != en {
			t.Errorf("lang %q: got %q, want English %q", lang, got, en)
		}
	}
}

// TestReliabilityMessageUnknownLocaleFallsBackToEn 验证未知或空语言环境回退到英文。
func TestReliabilityMessageUnknownLocaleFallsBackToEn(t *testing.T) {
	want := "output clipped to 7 bytes"
	for _, lang := range []string{"fr-FR", "ja-JP", "not-a-locale", ""} {
		if got := ReliabilityMessage(lang, ReliabilityOutputClipped, 7); got != want {
			t.Errorf("lang %q: got %q, want fallback %q", lang, got, want)
		}
	}
}

// TestReliabilityMessageMissingKey 验证未知键返回带键的稳定占位串且不 panic。
func TestReliabilityMessageMissingKey(t *testing.T) {
	for _, lang := range []string{EnUS, ZhCN, "fr-FR", "中文", ""} {
		const key = "reliability.brand_new"
		first := ReliabilityMessage(lang, key, "ignored-arg")
		second := ReliabilityMessage(lang, key)
		if first == "" || first != second {
			t.Errorf("lang %q: unstable placeholder: %q vs %q", lang, first, second)
		}
		if !strings.Contains(first, key) {
			t.Errorf("lang %q: placeholder %q does not contain key %q", lang, first, key)
		}
	}
}

// TestReliabilityMessageKeysSortedAndStable 验证键列表稳定排序、覆盖全部键且返回独立切片。
func TestReliabilityMessageKeysSortedAndStable(t *testing.T) {
	got := ReliabilityMessageKeys()
	again := ReliabilityMessageKeys()
	if !reflect.DeepEqual(got, again) {
		t.Fatalf("ReliabilityMessageKeys not stable: %v vs %v", got, again)
	}
	if !sort.StringsAreSorted(got) {
		t.Errorf("ReliabilityMessageKeys not sorted: %v", got)
	}

	want := make([]string, 0, len(reliabilityMessageTable))
	for key := range reliabilityMessageTable {
		want = append(want, key)
	}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReliabilityMessageKeys = %v, want %v", got, want)
	}

	// 修改返回的切片不应影响表或后续调用。
	if len(got) > 0 {
		got[0] = "corrupted"
	}
	if !reflect.DeepEqual(ReliabilityMessageKeys(), want) {
		t.Errorf("mutating returned keys affected later calls: %v", ReliabilityMessageKeys())
	}
}

// TestReliabilityMessageCJK 验证含 CJK 的文案与键保持完整 UTF-8 字节序列。
func TestReliabilityMessageCJK(t *testing.T) {
	for _, key := range ReliabilityMessageKeys() {
		zh := ReliabilityMessage(ZhCN, key)
		if !utf8.ValidString(zh) || !containsCJK(zh) {
			t.Errorf("key %q: Chinese message not valid CJK: %q", key, zh)
		}
	}

	// 未知 CJK 键应原样出现在占位串中，不丢失或截断任何字节。
	const cjkKey = "reliability.配额不足：链接已失效 🪙"
	for _, lang := range []string{EnUS, ZhCN, "中文"} {
		got := ReliabilityMessage(lang, cjkKey)
		if !utf8.ValidString(got) || !strings.Contains(got, cjkKey) {
			t.Errorf("lang %q: CJK key corrupted in placeholder: %q", lang, got)
		}
	}
}

// TestReliabilityMessageDoesNotMutateArgs 验证调用不修改传入的参数切片或其指向的数据。
func TestReliabilityMessageDoesNotMutateArgs(t *testing.T) {
	args := []any{"rm -rf /tmp", 7}
	original := append([]any(nil), args...)
	_ = ReliabilityMessage(ZhCN, ReliabilityApprovalDenied, args...)
	if !reflect.DeepEqual(args, original) {
		t.Errorf("args slice mutated: got %v, want %v", args, original)
	}

	data := []int{1, 2, 3}
	snapshot := []int{1, 2, 3}
	_ = ReliabilityMessage(EnUS, ReliabilityCompactionImpact, &data, 7)
	if !reflect.DeepEqual(data, snapshot) {
		t.Errorf("pointed-to data mutated: got %v, want %v", data, snapshot)
	}
}

// TestReliabilityMessagePure 验证 ReliabilityMessage 不向 Missing 记录任何内容。
func TestReliabilityMessagePure(t *testing.T) {
	ResetMissing()
	_ = ReliabilityMessage(ZhCN, ReliabilityCompactionLossy, 1)
	_ = ReliabilityMessage(EnUS, "reliability.brand_new")
	if got := Missing(); len(got) != 0 {
		t.Errorf("ReliabilityMessage recorded missing keys: %v", got)
	}
}
