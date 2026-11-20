package tui

import (
	"sync"
	"testing"
	"time"
)

// TestNoticeSeverityGrading 检查八个级别是否仿照 Crush 的八个
// InfoType 类别并带有稳定、互异的显示优先级。
func TestNoticeSeverityGrading(t *testing.T) {
	levels := []Severity{
		SeverityInfo, SeveritySuccess, SeverityUpdate, SeverityPlan,
		SeverityYolo, SeveritySysadmin, SeverityWarn, SeverityError,
	}
	seen := make(map[Severity]bool, len(levels))
	for i, sev := range levels {
		if int(sev) != i {
			t.Fatalf("severity %q has value %d, want %d", sev, sev, i)
		}
		if seen[sev] {
			t.Fatalf("duplicate severity %q", sev)
		}
		seen[sev] = true
		if sev.String() == "" || sev.String() == "unknown" {
			t.Fatalf("severity %d has no name", sev)
		}
	}
	if !(SeverityError > SeverityWarn && SeverityWarn > SeverityInfo) {
		t.Fatal("errors must outrank warnings, which outrank info")
	}
}

// TestNoticeStackSeverityPriority 检查 Peek 选出最高严重度，
// 与推送顺序无关，且同等严重度时按最新 seq 打破平局。
func TestNoticeStackSeverityPriority(t *testing.T) {
	base := time.Unix(1_000, 0)
	var s NoticeStack

	if _, ok := s.Peek(); ok {
		t.Fatal("empty stack must not yield a notice")
	}

	s.ReportInfo("low priority", time.Minute, base)
	got, ok := s.Peek()
	if !ok || got.Text != "low priority" {
		t.Fatalf("first push = %q, %v", got.Text, ok)
	}

	errNotice := s.ReportError("disk full", time.Minute, base)
	got, _ = s.Peek()
	if got.Seq != errNotice.Seq || got.Severity != SeverityError {
		t.Fatalf("higher severity must win: got %+v", got)
	}

	// 较晚的低严重度通知不得取代较早的高严重度
	// 通知：严重度不同时，由严重度而非新旧决定。
	s.ReportInfo("just info", time.Minute, base)
	got, _ = s.Peek()
	if got.Seq != errNotice.Seq {
		t.Fatalf("new low-severity notice covered an older error: got seq %d", got.Seq)
	}

	// 同等严重度时，较新的胜出。
	first := s.ReportWarn("older warn", time.Minute, base)
	second := s.ReportWarn("newer warn", time.Minute, base)
	s.Clear(errNotice.Seq)
	got, _ = s.Peek()
	if got.Seq != second.Seq || got.Seq <= first.Seq {
		t.Fatalf("equal-severity tie must prefer newest: got %+v", got)
	}
}

// TestNoticeStackTTLExpiry 检查每条通知的 TTL、边界行为以及
// 未提供时施加的默认 TTL。
func TestNoticeStackTTLExpiry(t *testing.T) {
	base := time.Unix(2_000, 0)
	var s NoticeStack

	s.Push(SeverityInfo, "short", 2*time.Second, base)
	long := s.Push(SeverityError, "long", 10*time.Second, base)

	if removed := s.Prune(base.Add(time.Second)); removed != 0 {
		t.Fatalf("nothing should expire at +1s, removed %d", removed)
	}
	if got, ok := s.Current(base.Add(3 * time.Second)); !ok || got.Seq != long.Seq {
		t.Fatalf("at +3s only the long notice should remain: %+v, %v", got, ok)
	}
	// 上面的 Current(+3s) 已清理了短 TTL 通知；这里只剩
	// 长 TTL 的那条到期。
	if removed := s.Prune(base.Add(11 * time.Second)); removed != 1 {
		t.Fatalf("the long notice should expire at +11s, removed %d", removed)
	}
	if _, ok := s.Peek(); ok {
		t.Fatal("stack must be empty after full expiry")
	}

	// 在恰好 TTL 边界处过期是含端点的。
	var d NoticeStack
	exact := d.Push(SeverityWarn, "boundary", time.Second, base)
	if exact.Expired(base.Add(time.Second - time.Nanosecond)) {
		t.Fatal("notice must still be live just before its TTL")
	}
	if !exact.Expired(base.Add(time.Second)) {
		t.Fatal("notice must be expired exactly at its TTL")
	}

	// 零 TTL 回退到 DefaultNoticeTTL。
	var def NoticeStack
	n := def.ReportInfo("default ttl", 0, base)
	if n.TTL != DefaultNoticeTTL {
		t.Fatalf("default TTL = %v, want %v", n.TTL, DefaultNoticeTTL)
	}
	if removed := def.Prune(base.Add(DefaultNoticeTTL)); removed != 1 {
		t.Fatalf("default-TTL notice should expire, removed %d", removed)
	}
}

// TestNoticeStackClearRetirement 检查按序号退休永不
// 让陈旧的清除退休较新的通知，且 seq 保持单调。
func TestNoticeStackClearRetirement(t *testing.T) {
	base := time.Unix(3_000, 0)
	var s NoticeStack

	old := s.ReportError("old error", time.Hour, base)
	newer := s.ReportInfo("new info", time.Hour, base)

	// 当前错误通知因严重度胜出；退休它后露出较新的那条。
	if got, _ := s.Peek(); got.Seq != old.Seq {
		t.Fatal("old error should be displayed before retirement")
	}
	if !s.Clear(old.Seq) {
		t.Fatal("Clear of a live notice must report removal")
	}
	if got, ok := s.Peek(); !ok || got.Seq != newer.Seq {
		t.Fatalf("retiring the old notice must reveal the newer one: %+v, %v", got, ok)
	}

	// 对旧 seq 的重复/陈旧清除是空操作，不能移除
	// 较新的通知。
	if s.Clear(old.Seq) {
		t.Fatal("clearing an already-retired seq must be a no-op")
	}
	if got, ok := s.Peek(); !ok || got.Seq != newer.Seq {
		t.Fatalf("stale clear retired a newer notice: %+v, %v", got, ok)
	}
	if s.Clear(9999) {
		t.Fatal("clearing an unknown seq must be a no-op")
	}

	// 序号在多次清除后仍单调。
	third := s.ReportWarn("third", time.Hour, base)
	if third.Seq != newer.Seq+1 || s.Seq() != third.Seq {
		t.Fatalf("seq not monotonic: third=%d stack=%d", third.Seq, s.Seq())
	}
	if !s.Clear(third.Seq) || !s.Clear(newer.Seq) {
		t.Fatal("retiring remaining live notices must succeed")
	}
	if s.Len() != 0 {
		t.Fatalf("stack should be empty, Len=%d", s.Len())
	}
}

// TestNoticeStackConcurrentAccess 从多个 goroutine 测试
// Push/Peek/Clear/Prune/Current；有互斥锁时所有操作安全且 seq 唯一。
func TestNoticeStackConcurrentAccess(t *testing.T) {
	const writers = 64
	base := time.Unix(4_000, 0)
	zero := time.Time{}
	var s NoticeStack

	var wg sync.WaitGroup
	seqs := make(chan uint64, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			n := s.Push(Severity(i%8), "msg", time.Hour, base)
			seqs <- n.Seq
		}(i)
	}

	// 并发读取者/退休者使用零时间，使 Prune 不移除任何内容。
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Peek()
			s.Current(zero)
			s.Len()
			s.Seq()
		}()
	}
	wg.Wait()
	close(seqs)

	seen := make(map[uint64]bool, writers)
	for seq := range seqs {
		if seq == 0 || seen[seq] {
			t.Fatalf("duplicate or zero sequence number: %d", seq)
		}
		seen[seq] = true
	}
	if s.Seq() != writers || s.Len() != writers {
		t.Fatalf("expected %d notices, got seq=%d len=%d", writers, s.Seq(), s.Len())
	}

	// 并发的 Clear 调用必须每条通知恰好退休一次，且不
	// 破坏切片。
	var clearWG sync.WaitGroup
	for seq := range seen {
		clearWG.Add(1)
		go func(seq uint64) {
			defer clearWG.Done()
			s.Clear(seq)
			s.Peek()
		}(seq)
	}
	clearWG.Wait()
	if s.Len() != 0 {
		t.Fatalf("all notices should be retired, Len=%d", s.Len())
	}
}

// TestNoticeStackCJKText 验证多语言文本被逐字存储和选中
// ；裁剪是渲染器的事，而非管理器的事。
func TestNoticeStackCJKText(t *testing.T) {
	base := time.Unix(5_000, 0)
	var s NoticeStack

	s.ReportInfo("已切换到深色主题", time.Minute, base)
	errNotice := s.ReportError("无法写入文件：权限被拒绝", time.Minute, base)

	got, ok := s.Current(base)
	if !ok || got.Seq != errNotice.Seq {
		t.Fatalf("CJK error must win: %+v, %v", got, ok)
	}
	if got.Text != "无法写入文件：权限被拒绝" {
		t.Fatalf("CJK text altered: %q", got.Text)
	}

	if !s.Clear(errNotice.Seq) {
		t.Fatal("CJK notice must be retirable by seq")
	}
	got, _ = s.Peek()
	if got.Text != "已切换到深色主题" {
		t.Fatalf("remaining CJK info notice = %q", got.Text)
	}
}
