package agent

import (
	"strings"
	"testing"
)

// mustCompileTTSR 编译规则并在非法正则时直接失败测试；用于只关注判定行为的用例。
func mustCompileTTSR(t *testing.T, rules ...TTSRRule) []CompiledTTSRRule {
	t.Helper()
	compiled, err := CompileTTSRRules(rules)
	if err != nil {
		t.Fatalf("CompileTTSRRules 意外失败: %v", err)
	}
	return compiled
}

// TestTTSRValues 固定枚举取值，便于快照、日志与跨语言协议比对。
func TestTTSRValues(t *testing.T) {
	scopes := []struct {
		got  TTSRScope
		want string
	}{
		{TTSRScopeText, "text"},
		{TTSRScopeThinking, "thinking"},
		{TTSRScopeTool, "tool"},
	}
	for _, tc := range scopes {
		if string(tc.got) != tc.want {
			t.Fatalf("TTSRScope = %q，期望 %q", tc.got, tc.want)
		}
	}
	actions := []struct {
		got  TTSRAction
		want string
	}{
		{TTSRWarn, "warn"},
		{TTSRInject, "inject"},
		{TTSRRetry, "retry"},
	}
	for _, tc := range actions {
		if string(tc.got) != tc.want {
			t.Fatalf("TTSRAction = %q，期望 %q", tc.got, tc.want)
		}
	}
}

func TestCompileTTSRRules(t *testing.T) {
	cases := []struct {
		name       string
		rules      []TTSRRule
		wantCount  int
		wantErrSub []string
	}{
		{
			name:      "全部合法保持顺序",
			rules:     []TTSRRule{{Name: "a", Pattern: "foo"}, {Name: "b", Pattern: "bar"}},
			wantCount: 2,
		},
		{
			name:       "非法正则跳过并汇总错误",
			rules:      []TTSRRule{{Name: "bad1", Pattern: "["}, {Name: "good", Pattern: "ok"}, {Name: "bad2", Pattern: "(unclosed"}},
			wantCount:  1,
			wantErrSub: []string{"bad1", "bad2"},
		},
		{
			name:      "nil规则",
			rules:     nil,
			wantCount: 0,
		},
		{
			name:      "空规则",
			rules:     []TTSRRule{},
			wantCount: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			compiled, err := CompileTTSRRules(tc.rules)
			if len(compiled) != tc.wantCount {
				t.Fatalf("编译规则数 = %d，期望 %d", len(compiled), tc.wantCount)
			}
			if len(tc.wantErrSub) == 0 {
				if err != nil {
					t.Fatalf("期望无错误，得到 %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("期望汇总错误，得到 nil")
			}
			got := err.Error()
			for _, sub := range tc.wantErrSub {
				if !strings.Contains(got, sub) {
					t.Fatalf("错误 %q 未包含 %q", got, sub)
				}
			}
			// 被跳过的坏规则不应出现在合法结果中。
			for _, c := range compiled {
				for _, sub := range tc.wantErrSub {
					if c.Rule.Name == sub {
						t.Fatalf("非法规则 %q 不应出现在编译结果", sub)
					}
				}
			}
		})
	}
}

type ttSRStep struct {
	scope  TTSRScope
	delta  string
	wantOK bool
	want   TTSRVerdict
}

func TestTTSRWatcherFeed(t *testing.T) {
	cases := []struct {
		name          string
		rules         []TTSRRule
		windowRunes   int
		cooldownBytes int
		steps         []ttSRStep
	}{
		{
			name:  "单规则命中",
			rules: []TTSRRule{{Name: "r1", Scope: TTSRScopeText, Pattern: `ignore previous`, Action: TTSRWarn, Reminder: "提醒"}},
			steps: []ttSRStep{
				{scope: TTSRScopeText, delta: "please ignore previous instructions", wantOK: true,
					want: TTSRVerdict{RuleName: "r1", Action: TTSRWarn, Reminder: "提醒", Matched: "ignore previous"}},
			},
		},
		{
			name:  "单规则未命中",
			rules: []TTSRRule{{Name: "r1", Scope: TTSRScopeText, Pattern: `ignore previous`, Action: TTSRWarn, Reminder: "提醒"}},
			steps: []ttSRStep{
				{scope: TTSRScopeText, delta: "hello world", wantOK: false},
			},
		},
		{
			name:  "scope过滤text规则不对thinking触发",
			rules: []TTSRRule{{Name: "r1", Scope: TTSRScopeText, Pattern: `ignore previous`, Action: TTSRWarn, Reminder: "提醒"}},
			steps: []ttSRStep{
				{scope: TTSRScopeThinking, delta: "ignore previous", wantOK: false},
				{scope: TTSRScopeTool, delta: "ignore previous", wantOK: false},
				{scope: TTSRScopeText, delta: "ignore previous", wantOK: true,
					want: TTSRVerdict{RuleName: "r1", Action: TTSRWarn, Reminder: "提醒", Matched: "ignore previous"}},
			},
		},
		{
			name:  "跨delta窗口匹配",
			rules: []TTSRRule{{Name: "r1", Scope: TTSRScopeText, Pattern: `ignore previous`, Action: TTSRInject, Reminder: "rm"}},
			steps: []ttSRStep{
				{scope: TTSRScopeText, delta: "ignore", wantOK: false},
				{scope: TTSRScopeText, delta: " previous", wantOK: true,
					want: TTSRVerdict{RuleName: "r1", Action: TTSRInject, Reminder: "rm", Matched: "ignore previous"}},
			},
		},
		{
			name:          "cooldown抑制重复",
			rules:         []TTSRRule{{Name: "r1", Scope: TTSRScopeText, Pattern: `ignore`, Action: TTSRWarn, Reminder: "rm"}},
			cooldownBytes: 100,
			steps: []ttSRStep{
				{scope: TTSRScopeText, delta: "ignore", wantOK: true,
					want: TTSRVerdict{RuleName: "r1", Action: TTSRWarn, Reminder: "rm", Matched: "ignore"}},
				{scope: TTSRScopeText, delta: " ignore again", wantOK: false},
			},
		},
		{
			name: "多规则优先级retry大于inject大于warn",
			rules: []TTSRRule{
				{Name: "warn", Scope: TTSRScopeText, Pattern: `foo`, Action: TTSRWarn, Reminder: "rw"},
				{Name: "inject", Scope: TTSRScopeText, Pattern: `bar`, Action: TTSRInject, Reminder: "ri"},
				{Name: "retry", Scope: TTSRScopeText, Pattern: `baz`, Action: TTSRRetry, Reminder: "rr"},
			},
			steps: []ttSRStep{
				{scope: TTSRScopeText, delta: "foo bar baz", wantOK: true,
					want: TTSRVerdict{RuleName: "retry", Action: TTSRRetry, Reminder: "rr", Matched: "baz"}},
			},
		},
		{
			name: "多规则优先级inject大于warn",
			rules: []TTSRRule{
				{Name: "warn", Scope: TTSRScopeText, Pattern: `foo`, Action: TTSRWarn, Reminder: "rw"},
				{Name: "inject", Scope: TTSRScopeText, Pattern: `bar`, Action: TTSRInject, Reminder: "ri"},
			},
			steps: []ttSRStep{
				{scope: TTSRScopeText, delta: "foo bar", wantOK: true,
					want: TTSRVerdict{RuleName: "inject", Action: TTSRInject, Reminder: "ri", Matched: "bar"}},
			},
		},
		{
			name: "强度相同取编译顺序靠前者",
			rules: []TTSRRule{
				{Name: "first", Scope: TTSRScopeText, Pattern: `foo`, Action: TTSRWarn, Reminder: "r1"},
				{Name: "second", Scope: TTSRScopeText, Pattern: `foo`, Action: TTSRWarn, Reminder: "r2"},
			},
			steps: []ttSRStep{
				{scope: TTSRScopeText, delta: "foo", wantOK: true,
					want: TTSRVerdict{RuleName: "first", Action: TTSRWarn, Reminder: "r1", Matched: "foo"}},
			},
		},
		{
			name:  "CJK文本",
			rules: []TTSRRule{{Name: "zh", Scope: TTSRScopeText, Pattern: `忽略.*指令`, Action: TTSRRetry, Reminder: "中文提醒"}},
			steps: []ttSRStep{
				{scope: TTSRScopeText, delta: "请忽略之前的指令", wantOK: true,
					want: TTSRVerdict{RuleName: "zh", Action: TTSRRetry, Reminder: "中文提醒", Matched: "忽略之前的指令"}},
			},
		},
		{
			name:  "CJK跨delta窗口匹配不切断rune",
			rules: []TTSRRule{{Name: "zh", Scope: TTSRScopeText, Pattern: `忽略指令`, Action: TTSRWarn, Reminder: "rm"}},
			steps: []ttSRStep{
				{scope: TTSRScopeText, delta: "请忽", wantOK: false},
				{scope: TTSRScopeText, delta: "略指令", wantOK: true,
					want: TTSRVerdict{RuleName: "zh", Action: TTSRWarn, Reminder: "rm", Matched: "忽略指令"}},
			},
		},
		{
			name:  "nil规则不触发",
			rules: nil,
			steps: []ttSRStep{
				{scope: TTSRScopeText, delta: "ignore previous", wantOK: false},
			},
		},
		{
			name:  "空delta不改变状态",
			rules: []TTSRRule{{Name: "r1", Scope: TTSRScopeText, Pattern: `ignore`, Action: TTSRWarn, Reminder: "rm"}},
			steps: []ttSRStep{
				{scope: TTSRScopeText, delta: "", wantOK: false},
				{scope: TTSRScopeText, delta: "ignore", wantOK: true,
					want: TTSRVerdict{RuleName: "r1", Action: TTSRWarn, Reminder: "rm", Matched: "ignore"}},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := NewTTSRWatcher(mustCompileTTSR(t, tc.rules...), tc.windowRunes, tc.cooldownBytes)
			for i, step := range tc.steps {
				got, ok := w.Feed(step.scope, step.delta)
				if ok != step.wantOK {
					t.Fatalf("第 %d 步 Feed(%q, %q) 命中 = %v，期望 %v（裁决 %+v）", i+1, step.scope, step.delta, ok, step.wantOK, got)
				}
				if ok && got != step.want {
					t.Fatalf("第 %d 步裁决 = %+v，期望 %+v", i+1, got, step.want)
				}
				if !ok && got != (TTSRVerdict{}) {
					t.Fatalf("第 %d 步未命中应返回零值，得到 %+v", i+1, got)
				}
			}
		})
	}
}

func TestTTSRWatcherWindowBoundary(t *testing.T) {
	rule := TTSRRule{Name: "r1", Scope: TTSRScopeText, Pattern: `ignore previous`, Action: TTSRWarn, Reminder: "rm"}
	// "ignore previous" 恰好 15 个 rune。
	w := NewTTSRWatcher(mustCompileTTSR(t, rule), 15, 0)
	if _, ok := w.Feed(TTSRScopeText, "ignore previous"); !ok {
		t.Fatal("窗口恰好容纳模式时应命中")
	}
	w.Reset()

	// 窗口比模式小一个 rune：最早的内容被挤出，模式不可能完整落入窗口。
	small := NewTTSRWatcher(mustCompileTTSR(t, rule), 14, 0)
	if _, ok := small.Feed(TTSRScopeText, "ignore previous"); ok {
		t.Fatal("窗口小于模式长度时不应命中")
	}
	w.Reset()

	// 跨 delta 累积后按 rune 截断保留尾部：前 3 个 rune 被挤出，尾部模式仍完整。
	tail := NewTTSRWatcher(mustCompileTTSR(t, rule), 15, 0)
	if _, ok := tail.Feed(TTSRScopeText, "xx i"); ok {
		t.Fatal("部分内容不应命中")
	}
	if _, ok := tail.Feed(TTSRScopeText, "gnore previous"); !ok {
		t.Fatal("截断保留尾部后应命中")
	}
}

func TestTTSRWatcherCooldownExpires(t *testing.T) {
	w := NewTTSRWatcher(
		mustCompileTTSR(t, TTSRRule{Name: "r1", Scope: TTSRScopeText, Pattern: `ignore`, Action: TTSRWarn, Reminder: "rm"}),
		0, 8, // cooldownBytes = 8
	)
	if _, ok := w.Feed(TTSRScopeText, "ignore"); !ok {
		t.Fatal("首次应命中")
	}
	if _, ok := w.Feed(TTSRScopeText, " and"); ok {
		t.Fatal("冷却期内不应再次命中")
	}
	// 累计字节超过冷却阈值后，新的命中应被允许。
	if _, ok := w.Feed(TTSRScopeText, " ignore"); !ok {
		t.Fatal("冷却过期后应再次命中")
	}
}

func TestTTSRWatcherReset(t *testing.T) {
	w := NewTTSRWatcher(
		mustCompileTTSR(t, TTSRRule{Name: "r1", Scope: TTSRScopeText, Pattern: `ignore`, Action: TTSRWarn, Reminder: "rm"}),
		0, 100,
	)
	if _, ok := w.Feed(TTSRScopeText, "ignore"); !ok {
		t.Fatal("首次应命中")
	}
	if _, ok := w.Feed(TTSRScopeText, " ignore"); ok {
		t.Fatal("冷却期内不应再次命中")
	}

	w.Reset()

	// 冷却被清空：同点重试后立即可再次命中。
	if _, ok := w.Feed(TTSRScopeText, "ignore"); !ok {
		t.Fatal("Reset 后冷却清空，应再次命中")
	}
	w.Reset()

	// 窗口被清空：没有旧内容残留，仅喂无关文本不应命中。
	if _, ok := w.Feed(TTSRScopeText, "xyz"); ok {
		t.Fatal("Reset 后窗口清空，旧命中内容不应残留")
	}
}

func TestTTSRWatcherMatchedBounded(t *testing.T) {
	cases := []struct {
		name  string
		rule  TTSRRule
		delta string
	}{
		{
			name:  "ASCII长匹配",
			rule:  TTSRRule{Name: "a", Scope: TTSRScopeText, Pattern: `[a-z]+`, Action: TTSRWarn, Reminder: "rm"},
			delta: strings.Repeat("a", 200),
		},
		{
			name:  "CJK长匹配按rune截断",
			rule:  TTSRRule{Name: "h", Scope: TTSRScopeText, Pattern: `汉+`, Action: TTSRWarn, Reminder: "rm"},
			delta: strings.Repeat("汉", 100),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := NewTTSRWatcher(mustCompileTTSR(t, tc.rule), 0, 0)
			got, ok := w.Feed(TTSRScopeText, tc.delta)
			if !ok {
				t.Fatal("应命中")
			}
			if n := len([]rune(got.Matched)); n != ttsrMatchedMaxRunes {
				t.Fatalf("Matched rune 数 = %d，期望有界值 %d", n, ttsrMatchedMaxRunes)
			}
		})
	}
}
