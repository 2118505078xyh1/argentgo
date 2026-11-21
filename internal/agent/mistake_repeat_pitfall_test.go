package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// setRepeatPitfallForTest 在测试期间设置跨情节踩坑 opt-in，并在结束/失败时恢复原值。
// 刻意不调用 t.Parallel()：这是进程级全局状态，必须与同包其它测试串行。
func setRepeatPitfallForTest(t *testing.T, enabled bool) {
	t.Helper()
	orig := repeatPitfallTrackingEnabled()
	SetRepeatPitfallTracking(enabled)
	t.Cleanup(func() { SetRepeatPitfallTracking(orig) })
}

// alphaToken 生成纯字母的互不相同的签名片段。刻意避开数字，防止被
// NormalizeErrorSignature 折叠成同一个占位符而让容量测试失效。
func alphaToken(i int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	return fmt.Sprintf("pitfall-%c%c", letters[i/26], letters[i%26])
}

// TestRepeatPitfallOffBaseline 锁定 opt-in 关闭时的逐字节不变基线：
// RecordSignature 是彻底的 no-op，不建映射、不改连续序列、不影响 Check 与 Reset；
// Snapshot 恒为 nil。
func TestRepeatPitfallOffBaseline(t *testing.T) {
	setRepeatPitfallForTest(t, false)

	tr := NewMistakeTracker(2, func(k MistakeKind, n int) string { return "G:" + string(k) })
	tr.RecordSignature("连接超时：重试 3 次")
	tr.RecordSignature("权限不足：无法写入")
	tr.RecordSignature("boom")

	if got := tr.Snapshot(10); got != nil {
		t.Fatalf("opt-in 关闭时 Snapshot() = %+v, want nil", got)
	}
	if tr.pitfallCounts != nil || tr.pitfallOrder != nil {
		t.Fatalf("opt-in 关闭时不应分配跨情节状态: counts=%v order=%v", tr.pitfallCounts, tr.pitfallOrder)
	}
	if got := tr.Count(); got != 0 {
		t.Fatalf("RecordSignature 不得改动连续序列, Count() = %d, want 0", got)
	}

	// 连续序列本身（Record/Check/Reset）在关闭时行为完全照旧。
	tr.Record(MistakeKindAPIError)
	if stop, _ := tr.Check(); stop {
		t.Fatal("1 次错误不应达到限制 2")
	}
	tr.Record(MistakeKindAPIError)
	if stop, action := tr.Check(); stop || action.Guidance != "G:api_error" {
		t.Fatalf("达到限制应带指引继续: (%v, %+v)", stop, action)
	}
	tr.Reset()
	if stop, action := tr.Check(); stop || action != (MistakeAction{}) {
		t.Fatalf("Reset 后应为 (false, 零值): (%v, %+v)", stop, action)
	}
}

// TestRepeatPitfallCrossResetAccumulation 验证核心语义：Reset 只清连续序列，
// 不清跨情节签名计数；同类错误（仅易变数字不同）跨多次 Reset 仍累计。
func TestRepeatPitfallCrossResetAccumulation(t *testing.T) {
	setRepeatPitfallForTest(t, true)

	tr := NewMistakeTracker(2, func(MistakeKind, int) string { return "RECOVER" })
	const errText = "连接超时：重试 3 次"

	// 第一个情节：两次同类错误达到限制。
	tr.RecordSignature(errText)
	tr.Record(MistakeKindAPIError)
	tr.RecordSignature("连接超时：重试 4 次") // 数字不同 -> 同一签名
	tr.Record(MistakeKindAPIError)
	if stop, action := tr.Check(); stop || action.Guidance != "RECOVER" {
		t.Fatalf("情节1 应带指引继续: (%v, %+v)", stop, action)
	}

	// 成功调用 -> Reset：连续序列清零，但跨情节计数保留。
	tr.Reset()
	if got := tr.Count(); got != 0 {
		t.Fatalf("Reset 后连续序列应为 0, 得到 %d", got)
	}
	if stop, action := tr.Check(); stop || action != (MistakeAction{}) {
		t.Fatalf("Reset 后 Check 应为 (false, 零值): (%v, %+v)", stop, action)
	}

	// 第二个情节 Again 踩同一个坑。
	tr.RecordSignature(errText)
	tr.Record(MistakeKindToolExecutionFailed)
	if stop, _ := tr.Check(); stop {
		t.Fatal("新情节只 1 次错误，不应达到限制")
	}

	got := tr.Snapshot(10)
	if len(got) != 1 {
		t.Fatalf("跨 Reset 后应只有 1 个签名, 得到 %+v", got)
	}
	if got[0].Count != 3 {
		t.Fatalf("跨 Reset 累计次数 = %d, want 3", got[0].Count)
	}
	wantSig := NormalizeErrorSignature(errText)
	if got[0].Signature != wantSig {
		t.Fatalf("签名 = %q, want %q", got[0].Signature, wantSig)
	}
	// 绝不保存原始错误文本。
	if strings.Contains(got[0].Signature, "3 次") || strings.Contains(got[0].Signature, "4 次") {
		t.Fatalf("签名里泄漏了原始错误文本: %q", got[0].Signature)
	}
}

// TestRepeatPitfallDifferentSignaturesIsolated 验证不同签名各自独立计数，
// 仅易变片段不同的错误折叠到同一签名。
func TestRepeatPitfallDifferentSignaturesIsolated(t *testing.T) {
	setRepeatPitfallForTest(t, true)

	tr := NewMistakeTracker(0)
	tr.RecordSignature("连接超时：重试 3 次")
	tr.RecordSignature("连接超时：重试 9 次") // 折叠进第一个
	tr.RecordSignature("权限不足：无法写入")
	tr.RecordSignature("permission denied on /a/b")

	got := tr.Snapshot(10)
	if len(got) != 3 {
		t.Fatalf("应有 3 个不同签名, 得到 %+v", got)
	}
	bySig := make(map[string]int, len(got))
	for _, p := range got {
		bySig[p.Signature] = p.Count
	}
	want := map[string]int{
		NormalizeErrorSignature("连接超时：重试 3 次"):               2,
		NormalizeErrorSignature("权限不足：无法写入"):                 1,
		NormalizeErrorSignature("permission denied on /a/b"): 1,
	}
	for sig, n := range want {
		if bySig[sig] != n {
			t.Errorf("签名 %q 次数 = %d, want %d（全部: %+v）", sig, bySig[sig], n, got)
		}
	}
}

// TestRepeatPitfallSnapshotTopSorting 验证 Snapshot 的次数降序 + 首次登记优先的
// 稳定排序、Top N 截断，以及返回切片是副本。
func TestRepeatPitfallSnapshotTopSorting(t *testing.T) {
	setRepeatPitfallForTest(t, true)

	tr := NewMistakeTracker(0)
	// 首次登记顺序：beta(0), alpha(1), gamma(2)；次数：beta=2, alpha=2, gamma=3。
	tr.RecordSignature("beta")
	tr.RecordSignature("alpha")
	tr.RecordSignature("gamma")
	tr.RecordSignature("beta")
	tr.RecordSignature("alpha")
	tr.RecordSignature("gamma")
	tr.RecordSignature("gamma")

	got := tr.Snapshot(0) // topN<=0 -> 默认上限
	want := []ErrorPattern{
		{Signature: "gamma", Count: 3},
		{Signature: "beta", Count: 2},
		{Signature: "alpha", Count: 2},
	}
	if len(got) != len(want) {
		t.Fatalf("Snapshot() 长度 = %d, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Snapshot()[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}

	// Top N 截断。
	top1 := tr.Snapshot(1)
	if len(top1) != 1 || top1[0] != want[0] {
		t.Fatalf("Snapshot(1) = %+v, want [%+v]", top1, want[0])
	}

	// 返回的是副本：改动它不影响内部状态。
	top1[0].Count = 999
	top1[0].Signature = "mutated"
	if again := tr.Snapshot(1); again[0] != want[0] {
		t.Fatalf("修改返回切片污染了内部状态: %+v", again)
	}
}

// TestRepeatPitfallCapacityEviction 验证容量上限与"淘汰最旧"语义：
// 登记超过 repeatPitfallCap 个不同签名后保留最新的 cap 个，最旧的被淘汰；
// 被淘汰的签名再次出现会作为新签名重新登记。
func TestRepeatPitfallCapacityEviction(t *testing.T) {
	setRepeatPitfallForTest(t, true)

	tr := NewMistakeTracker(0)
	total := repeatPitfallCap + 10
	for i := 0; i < total; i++ {
		tr.RecordSignature(alphaToken(i))
	}

	if len(tr.pitfallCounts) != repeatPitfallCap {
		t.Fatalf("容量应被钳制在 %d, 得到 %d", repeatPitfallCap, len(tr.pitfallCounts))
	}
	all := tr.Snapshot(total)
	if len(all) != repeatPitfallCap {
		t.Fatalf("Snapshot 条目数 = %d, want %d", len(all), repeatPitfallCap)
	}
	present := make(map[string]bool, len(all))
	for _, p := range all {
		if p.Count != 1 {
			t.Errorf("签名 %q 次数 = %d, want 1", p.Signature, p.Count)
		}
		present[p.Signature] = true
	}
	// 最旧的 10 个（i=0..9）被淘汰，其余保留。
	for i := 0; i < 10; i++ {
		if present[alphaToken(i)] {
			t.Errorf("最旧签名 %q 应被淘汰", alphaToken(i))
		}
	}
	for i := 10; i < total; i++ {
		if !present[alphaToken(i)] {
			t.Errorf("较新签名 %q 应保留", alphaToken(i))
		}
	}

	// 被淘汰的签名再次出现：重新登记，并淘汰当前最旧（i=10）。
	tr.RecordSignature(alphaToken(0))
	if got, ok := tr.pitfallCounts[alphaToken(0)]; !ok || got != 1 {
		t.Fatalf("重新登记的签名次数 = %d (存在=%v), want 1", got, ok)
	}
	if _, ok := tr.pitfallCounts[alphaToken(10)]; ok {
		t.Fatal("新签名登记应淘汰当前最旧的签名 alphaToken(10)")
	}
	if len(tr.pitfallCounts) != repeatPitfallCap {
		t.Fatalf("重新登记后容量 = %d, want %d", len(tr.pitfallCounts), repeatPitfallCap)
	}
}

// TestRepeatPitfallCJK 验证 CJK/emoji 原样保留、跨 rune 安全截断，
// 且 Snapshot 中的签名恰为规范化结果。
func TestRepeatPitfallCJK(t *testing.T) {
	setRepeatPitfallForTest(t, true)

	tr := NewMistakeTracker(0)
	a := "连接超时：重试 3 次 😵"
	b := "连接超时：重试 7 次 😵"
	tr.RecordSignature(a)
	tr.RecordSignature(b)

	got := tr.Snapshot(10)
	if len(got) != 1 {
		t.Fatalf("CJK 仅数字不同的错误应折叠为 1 个签名, 得到 %+v", got)
	}
	if got[0].Count != 2 {
		t.Fatalf("CJK 签名次数 = %d, want 2", got[0].Count)
	}
	if !strings.Contains(got[0].Signature, "连接超时") || !strings.Contains(got[0].Signature, "😵") {
		t.Fatalf("CJK/emoji 应原样保留, 得到 %q", got[0].Signature)
	}
	if want := NormalizeErrorSignature(a); got[0].Signature != want {
		t.Fatalf("CJK 签名 = %q, want %q", got[0].Signature, want)
	}

	// 超长 CJK 文本按 rune 截断，不会产生半个多字节字符。
	long := strings.Repeat("错误", 400)
	tr.RecordSignature(long)
	sig := NormalizeErrorSignature(long)
	if len([]rune(sig)) != maxSignatureRunes {
		t.Fatalf("超长签名 rune 数 = %d, want %d", len([]rune(sig)), maxSignatureRunes)
	}
}

// TestRepeatPitfallDoesNotChangeBehavior 验证 opt-in 开启也不会注入指引或改变
// Check/停止行为：连续序列、指引、停止判定与关闭时逐字节一致。
func TestRepeatPitfallDoesNotChangeBehavior(t *testing.T) {
	guidance := func(k MistakeKind, n int) string { return fmt.Sprintf("recover %s/%d", k, n) }

	run := func(withSignature bool) (stop bool, action MistakeAction, count int, perKind int) {
		setRepeatPitfallForTest(t, withSignature)
		tr := NewMistakeTracker(2, guidance)
		for i := 0; i < 2; i++ {
			if withSignature {
				tr.RecordSignature(fmt.Sprintf("某种失败：第 %d 次", i+1))
			}
			tr.Record(MistakeKindInvalidToolCall)
		}
		stop, action = tr.Check()
		count = tr.Count()
		perKind = tr.CountFor(MistakeKindInvalidToolCall)
		tr.Reset()
		if withSignature {
			tr.RecordSignature("另一种失败：重置后")
		}
		tr.Record(MistakeKindAPIError)
		return stop, action, count, perKind
	}

	offStop, offAction, offCount, offPer := run(false)
	onStop, onAction, onCount, onPer := run(true)
	if onStop != offStop || onAction != offAction || onCount != offCount || onPer != offPer {
		t.Fatalf("opt-in 开启了却改变了行为:\n off=(%v,%+v,%d,%d)\n on =(%v,%+v,%d,%d)",
			offStop, offAction, offCount, offPer, onStop, onAction, onCount, onPer)
	}
	if onStop || onAction.Guidance == "" {
		t.Fatalf("带指引达到限制应继续并携带指引, 得到 (%v, %+v)", onStop, onAction)
	}
}

// TestRepeatPitfallNilAndContextSafety 锁定 nil 接收方安全，且 RecordSignature
// 既不触碰 context 取消、也不产生事件（本类型不感知外部世界）。
func TestRepeatPitfallNilAndContextSafety(t *testing.T) {
	setRepeatPitfallForTest(t, true)

	var tr *MistakeTracker
	tr.RecordSignature("boom")
	if got := tr.Snapshot(10); got != nil {
		t.Fatalf("nil 接收方 Snapshot() = %+v, want nil", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	<-ctx.Done()
	live := NewMistakeTracker(0)
	live.RecordSignature("boom")
	if got := live.Snapshot(10); len(got) != 1 || got[0].Count != 1 {
		t.Fatalf("RecordSignature 不受 context 影响, 得到 %+v", got)
	}
}

// TestRepeatPitfallConcurrent 让多个 goroutine 并发 RecordSignature 与 Snapshot，
// 带 -race 能捕获竞争，精确断言确认无更新丢失。
func TestRepeatPitfallConcurrent(t *testing.T) {
	setRepeatPitfallForTest(t, true)

	const (
		goroutines = 12
		perRoutine = 80
	)
	tr := NewMistakeTracker(0)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perRoutine; i++ {
				tr.RecordSignature("连接超时：重试 42 次")
				if i%13 == 0 {
					_ = tr.Snapshot(3)
				}
			}
		}()
	}
	wg.Wait()

	got := tr.Snapshot(10)
	if len(got) != 1 || got[0].Count != goroutines*perRoutine {
		t.Fatalf("并发计数 = %+v, want 单个签名次数 %d", got, goroutines*perRoutine)
	}
}

// TestRepeatPitfallOptInSetterConcurrent 验证 opt-in 开关底层是原子的：
// 写端反复切换、读端反复读取，在 -race 下无数据竞争。
func TestRepeatPitfallOptInSetterConcurrent(t *testing.T) {
	orig := repeatPitfallTrackingEnabled()
	t.Cleanup(func() { SetRepeatPitfallTracking(orig) })

	const rounds = 200
	start := make(chan struct{})
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			<-start
			for i := 0; i < rounds; i++ {
				SetRepeatPitfallTracking((i+w)%2 == 0)
			}
		}(w)
	}
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < rounds; i++ {
				_ = repeatPitfallTrackingEnabled()
			}
		}()
	}
	close(start)
	wg.Wait()
}
