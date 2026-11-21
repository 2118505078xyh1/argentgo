package app

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/session"
)

// 本文件守护本次两处 opt-in 接线的底线：
//   - 无证据压缩走统一入口（fidelityWithoutEvidence）；
//   - 本地裁剪省够则跳过模型摘要（localTrimFirst）。
//
// 默认关闭时帧/事件/记录逐字节不变；显式开启时两条新路径均可达、不 mutating 入参、
// 且并发与重复调用结果稳定。所有测试不联网、不读进程环境、不用真实模型。

// wireTestRes 构造一个触发阈值极低、百分比关闭、raw 可控的解析结果。
func wireTestRes(t *testing.T, raw map[string]any) Resolved {
	t.Helper()
	env, _ := testEnv(t)
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	res.AutoCompactTokens = 1
	res.Cfg.ContextWindow = 0
	res.Cfg.AutoCondenseContextPercent = 0
	res.Cfg.Raw = raw
	return res
}

// wireTestSession 把同一批消息追加到新会话，供两条路径做字节对比。
func wireTestSession(t *testing.T, res Resolved, name string, msgs []llm.Message) *session.Session {
	t.Helper()
	sess, err := testStore(t, res).NewSession(name)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if err := sess.Append(m.Role, map[string]any{
			"content": m.Content, "tool_call_id": m.ToolCallID,
		}); err != nil {
			t.Fatal(err)
		}
	}
	return sess
}

// wireRecordPairs 只取 kind 与 data，跳过每次运行都不同的 id/parent/ts，
// 以及会话名/创建时间各自不同的 schema/header 记录，使两个会话可以做逐字节对比。
func wireRecordPairs(t *testing.T, sess *session.Session) []string {
	t.Helper()
	records, err := sess.Records()
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(records))
	for _, r := range records {
		if r.Kind == "schema" || r.Kind == "header" {
			continue
		}
		out = append(out, r.Kind+"\x00"+string(r.Data))
	}
	return out
}

// noEvidenceMessages 的被丢弃前缀含一条用户目标；直接 Compact 与统一管线
// 重建后的摘要形态不同，便于断言两条路径确实被走到。
func noEvidenceMessages() []llm.Message {
	return []llm.Message{
		{Role: "system", Content: "rules"},
		{Role: "user", Content: "修复登录鉴权（中文目标）"},
		{Role: "assistant", Content: "分析完成"},
		{Role: "tool", Content: "read foo.go"},
		{Role: "assistant", Content: "继续"},
		{Role: "user", Content: "q1"},
		{Role: "assistant", Content: "a1"},
		{Role: "user", Content: "q2"},
		{Role: "assistant", Content: "a2"},
	}
}

// localTrimDropped 含一条超长工具结果，本地裁剪默认预算即可省下远超 30%。
func localTrimDropped() []session.Entry {
	return []session.Entry{
		{Role: "system", Content: "rules"},
		{Role: "user", Content: "goal"},
		toolEntry(strings.Repeat("T", 20000)),
		{Role: "assistant", Content: "done"},
	}
}

const localTrimReply = `{"goal":"g","narrative":"模型叙事","next_steps":["补测试"]}`

// localTrimCountingChatter 只统计调用次数并返回一个确定回复；用原子计数保证并发安全。
type localTrimCountingChatter struct {
	calls atomic.Int32
}

func (c *localTrimCountingChatter) Chat(ctx context.Context, model string, messages []llm.Message, tools []llm.ToolSpec, onText func(string)) (llm.Message, error) {
	c.calls.Add(1)
	return llm.Message{Content: localTrimReply}, nil
}

// TestNoEvidenceFidelityEnabledGateShapes 锁定无证据统一入口的 opt-in 形态：
// 只有显式布尔 true 才开启，其他任何形式（含缺键、错类型、其他键）都关闭。
func TestNoEvidenceFidelityEnabledGateShapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  map[string]any
		want bool
	}{
		{"missing", nil, false},
		{"empty", map[string]any{}, false},
		{"nested true", map[string]any{"compaction": map[string]any{"fidelityWithoutEvidence": true}}, true},
		{"nested false", map[string]any{"compaction": map[string]any{"fidelityWithoutEvidence": false}}, false},
		{"nested other key", map[string]any{"compaction": map[string]any{"archive": true}}, false},
		{"top camel", map[string]any{"fidelityWithoutEvidence": true}, true},
		{"top kebab", map[string]any{"fidelity-without-evidence": true}, true},
		{"wrong type", map[string]any{"compaction": map[string]any{"fidelityWithoutEvidence": "true"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := noEvidenceFidelityEnabled(tc.raw); got != tc.want {
				t.Fatalf("noEvidenceFidelityEnabled(%v) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

// TestLocalTrimFirstEnabledGateShapes 锁定本地裁剪优先的 opt-in 形态。
func TestLocalTrimFirstEnabledGateShapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  map[string]any
		want bool
	}{
		{"missing", nil, false},
		{"empty", map[string]any{}, false},
		{"nested true", map[string]any{"compaction": map[string]any{"localTrimFirst": true}}, true},
		{"nested false", map[string]any{"compaction": map[string]any{"localTrimFirst": false}}, false},
		{"other compaction key", map[string]any{"compaction": map[string]any{"archive": true}}, false},
		{"top camel", map[string]any{"localTrimFirst": true}, true},
		{"top kebab", map[string]any{"local-trim-first": true}, true},
		{"wrong type", map[string]any{"localTrimFirst": 1}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := localTrimFirstEnabled(tc.raw); got != tc.want {
				t.Fatalf("localTrimFirstEnabled(%v) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

// TestMaybeAutoCompactNoEvidenceDefaultIsByteExact 守护默认底线：无证据且 opt-in 关闭时，
// maybeAutoCompact 产生的记录必须与直接 sess.Compact(defaultCompactKeep) 逐字节一致。
func TestMaybeAutoCompactNoEvidenceDefaultIsByteExact(t *testing.T) {
	res := wireTestRes(t, nil)
	msgs := noEvidenceMessages()
	gotSess := wireTestSession(t, res, "default-wire", msgs)
	wantSess := wireTestSession(t, res, "direct-compact", msgs)

	maybeAutoCompact(res, gotSess, agent.CompactionEvidence{}, nil, nil)
	if err := wantSess.Compact(defaultCompactKeep); err != nil {
		t.Fatal(err)
	}

	got, want := wireRecordPairs(t, gotSess), wireRecordPairs(t, wantSess)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("默认无证据路径记录字节变化:\n got  %q\n want %q", got, want)
	}
}

// TestMaybeAutoCompactNoEvidenceOptInUsesUnifiedPipeline 证明 opt-in 开启后，
// 无证据压缩确实改走 compactWithEvidence：保真观察器追加告警，Goal 段被恢复，
// 而默认路径（同一个 reg）不会产生保真告警。
func TestMaybeAutoCompactNoEvidenceOptInUsesUnifiedPipeline(t *testing.T) {
	onRes := wireTestRes(t, map[string]any{"compaction": map[string]any{"fidelityWithoutEvidence": true}})
	offRes := wireTestRes(t, nil)
	msgs := noEvidenceMessages()
	onSess := wireTestSession(t, onRes, "unified", msgs)
	offSess := wireTestSession(t, offRes, "direct", msgs)

	reg := new(OptInRegistry)
	reg.Set(OptInPreserveSummarySections, true)
	reg.Set(OptInKeepImplicitContext, true)
	reg.Set(OptInCompactionFidelity, true)

	maybeAutoCompact(onRes, onSess, agent.CompactionEvidence{}, reg, nil)
	maybeAutoCompact(offRes, offSess, agent.CompactionEvidence{}, reg, nil)

	onComp := latestCompaction(t, onSess)
	offComp := latestCompaction(t, offSess)
	if !strings.Contains(onComp.Summary, compactionFidelityNoticePrefix) {
		t.Fatalf("opt-in 无证据未走统一管线（缺少保真告警）: %q", onComp.Summary)
	}
	if strings.Contains(offComp.Summary, compactionFidelityNoticePrefix) {
		t.Fatalf("默认路径不应挂接保真观察者: %q", offComp.Summary)
	}
	if !strings.Contains(onComp.Summary, "修复登录鉴权（中文目标）") {
		t.Fatalf("统一管线未保留原始 Goal: %q", onComp.Summary)
	}
}

// TestLocalTrimSkipDefaultKeepsModelCallAndBytes 守护本地裁剪默认底线：
// 即使 dropped 里有大量可裁剪内容，opt-in 关闭时仍调用模型，摘要与事件与接线前一致。
func TestLocalTrimSkipDefaultKeepsModelCallAndBytes(t *testing.T) {
	extractive := "## Compaction\n- dropped 4 message(s), kept the last 1\n"
	reg := modelSummaryOnReg()
	chatter := &localTrimCountingChatter{}
	var mu sync.Mutex
	var events []agent.Event
	emit := func(e agent.Event) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, e)
	}

	got := applyModelSummaryWireWithLocalTrim(extractive, localTrimDropped(), nil, reg,
		context.Background(), chatter, "mini", nil, nil, nil, emit)
	want := applyModelSummaryWire(extractive, reg, context.Background(),
		&localTrimCountingChatter{}, "mini", nil, nil, nil, nil)
	if got != want {
		t.Fatalf("默认关闭时摘要字节变化:\n got  %q\n want %q", got, want)
	}
	if chatter.calls.Load() != 1 {
		t.Fatalf("默认关闭时模型调用次数 = %d, want 1", chatter.calls.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 1 || events[0].Kind != agent.EventUsage {
		t.Fatalf("默认关闭时事件 = %+v, want 单个 EventUsage", events)
	}
}

// TestLocalTrimSkipWhenSavingsEnough 证明 opt-in 开启且本地裁剪省够时跳过模型调用，
// 不发事件；没有可裁剪内容时仍照常调用模型。
func TestLocalTrimSkipWhenSavingsEnough(t *testing.T) {
	extractive := "## Compaction\n- dropped 4 message(s), kept the last 1\n"
	raw := map[string]any{"compaction": map[string]any{"localTrimFirst": true}}
	reg := modelSummaryOnReg()

	chatter := &localTrimCountingChatter{}
	events := 0
	got := applyModelSummaryWireWithLocalTrim(extractive, localTrimDropped(), raw, reg,
		context.Background(), chatter, "mini", nil, nil, nil, func(agent.Event) { events++ })
	if got != extractive {
		t.Fatalf("裁剪达标应原样返回抽取式摘要: %q", got)
	}
	if chatter.calls.Load() != 0 || events != 0 {
		t.Fatalf("裁剪达标应跳过模型调用与事件: calls=%d events=%d", chatter.calls.Load(), events)
	}

	// 控制组：开关开启但没有超长工具结果，必须维持原有模型调用。
	shortChatter := &localTrimCountingChatter{}
	short := []session.Entry{{Role: "tool", Content: "short output"}}
	got2 := applyModelSummaryWireWithLocalTrim(extractive, short, raw, reg,
		context.Background(), shortChatter, "mini", nil, nil, nil, nil)
	if got2 == extractive {
		t.Fatalf("未省够时不应跳过模型摘要: %q", got2)
	}
	if shortChatter.calls.Load() != 1 {
		t.Fatalf("未省够时模型调用次数 = %d, want 1", shortChatter.calls.Load())
	}
}

// TestMaybeCompactSkipsModelSummaryWithLocalTrim 端到端验证接线点：轮中压缩在
// opt-in 开启时跳过模型摘要；默认关闭时仍调用一次。
func TestMaybeCompactSkipsModelSummaryWithLocalTrim(t *testing.T) {
	msgs := []llm.Message{
		{Role: "system", Content: "rules"},
		{Role: "user", Content: "goal"},
		{Role: "assistant", Content: "plan"},
		{Role: "tool", Content: strings.Repeat("T", 20000)},
		{Role: "assistant", Content: "step"},
		{Role: "user", Content: "q1"},
		{Role: "assistant", Content: "a1"},
		{Role: "user", Content: "q2"},
		{Role: "assistant", Content: "a2"},
	}

	run := func(name string, raw map[string]any) (int, bool, string) {
		res := wireTestRes(t, raw)
		sess := wireTestSession(t, res, name, msgs)
		chatter := &localTrimCountingChatter{}
		comp := newSessionCompactorWithOpts(res, sess, modelSummaryOnReg())
		comp.ctx = context.Background()
		comp.chatter = chatter
		comp.summaryModel = "mini"
		_, changed := comp.MaybeCompact(msgs, 80)
		return int(chatter.calls.Load()), changed, latestCompaction(t, sess).Summary
	}

	onCalls, onChanged, onSummary := run("trim-on", map[string]any{"compaction": map[string]any{"localTrimFirst": true}})
	offCalls, offChanged, offSummary := run("trim-off", nil)
	if !onChanged || !offChanged {
		t.Fatalf("两端都应发生压缩: on=%v off=%v", onChanged, offChanged)
	}
	if onCalls != 0 {
		t.Fatalf("opt-in 开启时模型调用次数 = %d, want 0", onCalls)
	}
	if offCalls != 1 {
		t.Fatalf("默认关闭时模型调用次数 = %d, want 1", offCalls)
	}
	if !strings.Contains(offSummary, modelSummaryMarker) {
		t.Fatalf("默认路径应带模型叙事标记: %q", offSummary)
	}
	if strings.Contains(onSummary, modelSummaryMarker) {
		t.Fatalf("跳过路径不应带模型叙事标记: %q", onSummary)
	}
}

// TestWireGatesConcurrentAndRepeatStable 验证并发/重复调用稳定：跳过路径不调用模型、
// 不修改入参，所有 goroutine 得到与串行执行逐字节相同的结果。
func TestWireGatesConcurrentAndRepeatStable(t *testing.T) {
	dropped := localTrimDropped()
	raw := map[string]any{"compaction": map[string]any{
		"localTrimFirst":          true,
		"fidelityWithoutEvidence": true,
	}}
	reg := modelSummaryOnReg()
	chatter := &localTrimCountingChatter{}
	extractive := "## Compaction\n- dropped 4 message(s), kept the last 1\n"

	want := applyModelSummaryWireWithLocalTrim(extractive, dropped, raw, reg,
		context.Background(), chatter, "mini", nil, nil, nil, nil)
	if chatter.calls.Load() != 0 {
		t.Fatalf("串行前置调用不应触发模型: calls=%d", chatter.calls.Load())
	}

	const n = 32
	results := make([]string, n)
	mutated := make([]bool, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			before := append([]session.Entry(nil), dropped...)
			results[i] = applyModelSummaryWireWithLocalTrim(extractive, dropped, raw, reg,
				context.Background(), chatter, "mini", nil, nil, nil, nil)
			mutated[i] = !reflect.DeepEqual(before, dropped)
		}(i)
	}
	wg.Wait()
	for i := 0; i < n; i++ {
		if results[i] != want {
			t.Fatalf("并发结果 %d 与串行不一致:\n got  %q\n want %q", i, results[i], want)
		}
		if mutated[i] {
			t.Fatalf("并发调用 %d 修改了 dropped 入参", i)
		}
	}
	if chatter.calls.Load() != 0 {
		t.Fatalf("并发跳过路径触发了模型调用: calls=%d", chatter.calls.Load())
	}

	// 重复调用的纯函数判定也稳定。
	for i := 0; i < 20; i++ {
		if !shouldSkipModelSummaryForLocalTrim(dropped, raw, reg) {
			t.Fatal("重复判定结果不稳定: 裁剪达标应始终跳过")
		}
		if !noEvidenceFidelityEnabled(raw) {
			t.Fatal("重复判定结果不稳定: opt-in 应始终开启")
		}
	}
}
