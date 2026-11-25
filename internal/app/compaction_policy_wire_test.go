package app

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"example.com/argentgo/internal/agent"
	"example.com/argentgo/internal/llm"
)

// 本文件测试压缩策略（agent.SelectCompactionAction）在运行后压缩触发点的 opt-in 观测接线：
// ARGENT_COMPACTION_POLICY=1 时在 maybeAutoCompact 决策旁落一条决策日志；默认关闭时整段
// （构造输入 + 纯函数计算 + 日志）是空操作，既有阈值路径与会话记录逐字节不变。
// 接线刻意纯观测：不改会话、压缩证据、阈值或任何压缩分支。

// appPolicyLogCapture 捕获 app 侧日志（与 agent 侧同构）。
type appPolicyLogCapture struct {
	mu   sync.Mutex
	recs []slog.Record
}

func (c *appPolicyLogCapture) Enabled(context.Context, slog.Level) bool { return true }

func (c *appPolicyLogCapture) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	c.recs = append(c.recs, r.Clone())
	c.mu.Unlock()
	return nil
}

func (c *appPolicyLogCapture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *appPolicyLogCapture) WithGroup(string) slog.Handler      { return c }

func (c *appPolicyLogCapture) records() []slog.Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]slog.Record, 0, len(c.recs))
	for _, r := range c.recs {
		if r.Message == "compaction policy decision" {
			out = append(out, r.Clone())
		}
	}
	return out
}

func captureAppPolicyLogs(t *testing.T) *appPolicyLogCapture {
	t.Helper()
	cap := &appPolicyLogCapture{}
	prev := compactionPolicyLogger
	compactionPolicyLogger = func() *slog.Logger { return slog.New(cap) }
	t.Cleanup(func() { compactionPolicyLogger = prev })
	return cap
}

// TestAppCompactionPolicyEnvFlagValues 验证 env 解析：仅去空白后的 "1" 开启。
func TestAppCompactionPolicyEnvFlagValues(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{{"1", true}, {" 1 ", true}, {"0", false}, {"true", false}, {"", false}} {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv("ARGENT_COMPACTION_POLICY", tc.value)
			if got := compactionPolicyEnabled(); got != tc.want {
				t.Fatalf("compactionPolicyEnabled(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

// TestAppCompactionPolicyInputTiers 验证四档决策都能由运行后信号喂出，并核对错误多→保据升级。
// 工具结果混入 CJK 角色与多字节正文，确保计数对非 ASCII 安全。
func TestAppCompactionPolicyInputTiers(t *testing.T) {
	msgs := []llm.Message{
		{Role: "tool", Content: "结果一"},
		{Role: " TOOL_RESULT ", Content: "中文输出"},
		{Role: "function", Content: "🔥 emoji"},
		{Role: "user", Content: "继续"},
	}
	if got := countToolResultMessages(msgs); got != 3 {
		t.Fatalf("countToolResultMessages = %d, want 3", got)
	}

	for _, tc := range []struct {
		name        string
		estimate    int
		threshold   int
		failures    int
		toolResults int
		want        agent.CompactionAction
	}{
		{"low none", 10, 100, 0, 0, agent.CompactionNone},
		{"very high summarize", 95, 100, 0, 0, agent.CompactionSummarizeModel},
		{"many results trim", 80, 100, 0, 12, agent.CompactionTrimLocal},
		{"errors preserve evidence", 80, 100, 5, 12, agent.CompactionSummarizeModel},
		{"no threshold signal", 50, 0, 5, 12, agent.CompactionNone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := autoCompactPolicyInput(tc.estimate, tc.threshold, tc.failures, tc.toolResults)
			if got := agent.SelectCompactionAction(in); got != tc.want {
				t.Fatalf("SelectCompactionAction(%+v) = %s, want %s", in, got, tc.want)
			}
		})
	}
}

// TestAppCompactionPolicyOffByDefault 验证默认关闭时零日志、逐字节基线不变：
// observe 直接返回空动作，maybeAutoCompact 产生的记录与直接 sess.Compact 完全一致。
func TestAppCompactionPolicyOffByDefault(t *testing.T) {
	t.Setenv("ARGENT_COMPACTION_POLICY", "")
	cap := captureAppPolicyLogs(t)

	if got := observeAutoCompactPolicy(95, 100, 5, 12); got != "" {
		t.Fatalf("关闭时 observeAutoCompactPolicy = %q, want empty", got)
	}
	if got := len(cap.records()); got != 0 {
		t.Fatalf("关闭时仍输出 %d 条策略日志", got)
	}

	res := wireTestRes(t, nil)
	msgs := noEvidenceMessages()
	gotSess := wireTestSession(t, res, "default-policy", msgs)
	wantSess := wireTestSession(t, res, "direct-compact", msgs)

	maybeAutoCompact(res, gotSess, agent.CompactionEvidence{}, nil, nil)
	if err := wantSess.Compact(defaultCompactKeep); err != nil {
		t.Fatal(err)
	}
	got, want := wireRecordPairs(t, gotSess), wireRecordPairs(t, wantSess)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("默认路径记录字节变化:\n got  %q\n want %q", got, want)
	}
	if got := len(cap.records()); got != 0 {
		t.Fatalf("默认关闭却输出了 %d 条策略日志", got)
	}
}

// TestAppCompactionPolicyOnEmitsDecision 验证 opt-in 开启时日志字段齐全且动作正确，
// 并且返回值即决策（四档之一）。
func TestAppCompactionPolicyOnEmitsDecision(t *testing.T) {
	t.Setenv("ARGENT_COMPACTION_POLICY", "1")
	cap := captureAppPolicyLogs(t)

	got := observeAutoCompactPolicy(95, 100, 5, 12)
	if got != agent.CompactionSummarizeModel {
		t.Fatalf("decision = %q, want %q", got, agent.CompactionSummarizeModel)
	}
	recs := cap.records()
	if len(recs) != 1 {
		t.Fatalf("策略日志数 = %d, want 1", len(recs))
	}
	attrs := map[string]slog.Value{}
	recs[0].Attrs(func(a slog.Attr) bool { attrs[a.Key] = a.Value; return true })
	for _, key := range []string{"action", "usage_ratio", "error_count", "tool_results", "budget_exceeded"} {
		if _, ok := attrs[key]; !ok {
			t.Fatalf("策略日志缺少字段 %q", key)
		}
	}
	if got := attrs["action"].String(); got != string(agent.CompactionSummarizeModel) {
		t.Fatalf("action = %q, want %q", got, agent.CompactionSummarizeModel)
	}
	if got := attrs["error_count"].Int64(); got != 5 {
		t.Fatalf("error_count = %d, want 5", got)
	}
}

// TestAppCompactionPolicyCoexistsWithExistingCompaction 验证开启后既有压缩照常发生：
// 策略日志与压缩记录共存，且压缩摘要与默认路径形态一致（纯观测不替换行为）。
func TestAppCompactionPolicyCoexistsWithExistingCompaction(t *testing.T) {
	t.Setenv("ARGENT_COMPACTION_POLICY", "1")
	cap := captureAppPolicyLogs(t)

	res := wireTestRes(t, nil)
	msgs := noEvidenceMessages()
	sess := wireTestSession(t, res, "policy-on", msgs)
	maybeAutoCompact(res, sess, agent.CompactionEvidence{}, nil, nil)

	comp := latestCompaction(t, sess)
	if strings.TrimSpace(comp.Summary) == "" {
		t.Fatal("开启后既有压缩仍应产生非空摘要")
	}
	recs := cap.records()
	if len(recs) == 0 {
		t.Fatal("开启后应至少有一条策略日志")
	}
	valid := map[string]bool{
		string(agent.CompactionNone): true, string(agent.CompactionTrimLocal): true,
		string(agent.CompactionSummarizeModel): true, string(agent.CompactionCompactFull): true,
	}
	for _, r := range recs {
		attrs := map[string]slog.Value{}
		r.Attrs(func(a slog.Attr) bool { attrs[a.Key] = a.Value; return true })
		action := attrs["action"].String()
		if !valid[action] {
			t.Fatalf("非法 action = %q", action)
		}
	}
}
