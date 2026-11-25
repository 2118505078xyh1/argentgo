package app

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"example.com/argentgo/internal/agent"
	"example.com/argentgo/internal/llm"
	"example.com/argentgo/internal/session"
)

// 本文件守护 integration.go 轮后压缩路径（compactWithEvidence / maybeAutoCompact）
// 的两处 opt-in 接线，与轮中路径（MaybeCompact）对齐：
//   - 无损归档：archive 开启时轮后也写归档并附加引用；
//   - 本地裁剪：localTrimFirst 开启且省够时轮后也跳过模型摘要。
//
// 底线：默认（opt-in 关闭、summaryDep 为 nil）时轮后记录逐字节不变。
// 所有测试不联网、不用真实模型。

// postRoundEvidence 提供一条失败与一条后续步骤，使 maybeAutoCompact 必定走
// compactWithEvidence 统一入口（而非默认的 sess.Compact 直通路径）。
func postRoundEvidence() agent.CompactionEvidence {
	return agent.CompactionEvidence{
		Failures:  []agent.Failure{{Turn: 1, Tool: "bash", Reason: "连接失败"}},
		NextSteps: []string{"补单元测试"},
	}
}

// postRoundMessages 构造 9 条消息：切点落在索引 3 之前，被丢弃前缀
// （索引 0..2）含一条超长工具结果，本地裁剪默认预算即可省下远超 30%。
func postRoundMessages() []llm.Message {
	return []llm.Message{
		{Role: "system", Content: "rules"},
		{Role: "tool", Content: strings.Repeat("T", 20000)}, // 落在被丢弃前缀里
		{Role: "assistant", Content: "plan"},
		{Role: "user", Content: "q1"},
		{Role: "assistant", Content: "a1"},
		{Role: "user", Content: "q2"},
		{Role: "assistant", Content: "a2"},
		{Role: "user", Content: "q3"},
		{Role: "assistant", Content: "a3"},
	}
}

// postRoundRun 在指定配置下跑一次轮后压缩并返回会话。withDep 为 false 时
// summaryDep 传 nil（模拟测试/默认装配面），为 true 时注入完整依赖的 compactor。
func postRoundRun(t *testing.T, name string, raw map[string]any, withDep bool, chatter *localTrimCountingChatter) *session.Session {
	t.Helper()
	res := wireTestRes(t, raw)
	sess := wireTestSession(t, res, name, postRoundMessages())
	var dep *sessionCompactor
	var reg *OptInRegistry
	if withDep {
		reg = new(OptInRegistry)
		if chatter != nil {
			reg = modelSummaryOnReg()
		}
		dep = newSessionCompactorWithOpts(res, sess, reg)
		dep.ctx = context.Background()
		dep.chatter = chatter
		dep.summaryModel = "mini"
		dep.emit = func(agent.Event) {}
	}
	maybeAutoCompact(res, sess, postRoundEvidence(), reg, dep)
	return sess
}

// TestPostRoundDefaultIsByteExact 守护默认底线：有证据走 compactWithEvidence 时，
// 传入一个全 opt-in 关闭的 summaryDep，与完全不传 summaryDep 的记录逐字节一致。
func TestPostRoundDefaultIsByteExact(t *testing.T) {
	withDep := postRoundRun(t, "postround-dep", nil, true, nil)
	withoutDep := postRoundRun(t, "postround-nil", nil, false, nil)

	got, want := wireRecordPairs(t, withDep), wireRecordPairs(t, withoutDep)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("默认轮后路径记录字节变化:\n got  %q\n want %q", got, want)
	}
	// 默认不得产生归档文件或归档通知。
	comp := latestCompaction(t, withDep)
	if comp.Archive != nil || strings.Contains(comp.Summary, "## Lossless archive") {
		t.Fatalf("默认路径不应归档: %+v / %q", comp.Archive, comp.Summary)
	}
	if _, err := os.Stat(filepath.Join(withDep.Dir(), "compactions")); !os.IsNotExist(err) {
		t.Fatalf("默认路径不应创建 compactions 目录 (stat err: %v)", err)
	}
}

// TestPostRoundArchivesWhenEnabled 证明 archive 开启后轮后路径也写无损归档，
// 并把引用与恢复通知附加到压缩记录；summaryDep 为 nil 时即便 raw 开着也不归档。
func TestPostRoundArchivesWhenEnabled(t *testing.T) {
	raw := map[string]any{"compaction": map[string]any{"archive": true}}

	on := postRoundRun(t, "postround-archive-on", raw, true, nil)
	comp := latestCompaction(t, on)
	if comp.Archive == nil {
		t.Fatal("archive 开启后轮后压缩未附加归档引用")
	}
	if !strings.HasPrefix(comp.Archive.Path, "compactions/") || !strings.HasSuffix(comp.Archive.Path, ".jsonl") {
		t.Fatalf("归档路径 = %q, want compactions/*.jsonl", comp.Archive.Path)
	}
	if !strings.Contains(comp.Summary, "## Lossless archive") || !strings.Contains(comp.Summary, comp.Archive.Path) {
		t.Fatalf("摘要缺少归档恢复通知: %q", comp.Summary)
	}
	onDisk := filepath.Join(on.Dir(), filepath.FromSlash(comp.Archive.Path))
	info, err := os.Stat(onDisk)
	if err != nil {
		t.Fatalf("归档文件缺失 %s: %v", onDisk, err)
	}
	if info.Size() != comp.Archive.Bytes {
		t.Fatalf("归档磁盘大小 = %d, ref 记录 = %d", info.Size(), comp.Archive.Bytes)
	}

	off := postRoundRun(t, "postround-archive-off", raw, false, nil)
	offComp := latestCompaction(t, off)
	if offComp.Archive != nil || strings.Contains(offComp.Summary, "## Lossless archive") {
		t.Fatalf("summaryDep 为 nil 时不应归档: %+v / %q", offComp.Archive, offComp.Summary)
	}
}

// TestPostRoundSkipsModelSummaryWithLocalTrim 证明 localTrimFirst 开启且省够时
// 轮后路径也跳过模型摘要（调用 0 次、无标记）；默认关闭时仍调用一次模型。
func TestPostRoundSkipsModelSummaryWithLocalTrim(t *testing.T) {
	run := func(name string, raw map[string]any) (int, string) {
		chatter := &localTrimCountingChatter{}
		sess := postRoundRun(t, name, raw, true, chatter)
		return int(chatter.calls.Load()), latestCompaction(t, sess).Summary
	}

	onCalls, onSummary := run("postround-trim-on", map[string]any{"compaction": map[string]any{"localTrimFirst": true}})
	offCalls, offSummary := run("postround-trim-off", nil)

	if onCalls != 0 {
		t.Fatalf("opt-in 开启时模型调用次数 = %d, want 0", onCalls)
	}
	if offCalls != 1 {
		t.Fatalf("默认关闭时模型调用次数 = %d, want 1", offCalls)
	}
	if strings.Contains(onSummary, modelSummaryMarker) {
		t.Fatalf("跳过路径不应带模型叙事标记: %q", onSummary)
	}
	if !strings.Contains(offSummary, modelSummaryMarker) {
		t.Fatalf("默认路径应带模型叙事标记: %q", offSummary)
	}
}
