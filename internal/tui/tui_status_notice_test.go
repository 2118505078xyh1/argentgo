package tui

import (
	"sync"
	"testing"
	"time"
)

// TestNoticeSeverityGrading checks that the eight levels mirror Crush's eight
// InfoType categories and carry a stable, distinct display priority.
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

// TestNoticeStackSeverityPriority checks Peek picks the highest severity,
// regardless of push order, and breaks equal-severity ties by the newest seq.
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

	// A later low-severity notice must not displace an earlier high-severity
	// one: severity, not recency, decides when severities differ.
	s.ReportInfo("just info", time.Minute, base)
	got, _ = s.Peek()
	if got.Seq != errNotice.Seq {
		t.Fatalf("new low-severity notice covered an older error: got seq %d", got.Seq)
	}

	// Among equal severities the newest wins.
	first := s.ReportWarn("older warn", time.Minute, base)
	second := s.ReportWarn("newer warn", time.Minute, base)
	s.Clear(errNotice.Seq)
	got, _ = s.Peek()
	if got.Seq != second.Seq || got.Seq <= first.Seq {
		t.Fatalf("equal-severity tie must prefer newest: got %+v", got)
	}
}

// TestNoticeStackTTLExpiry checks per-notice TTL, boundary behaviour and the
// default TTL applied when none is supplied.
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
	// Current(+3s) above already pruned the short notice; only the long one
	// is left to expire here.
	if removed := s.Prune(base.Add(11 * time.Second)); removed != 1 {
		t.Fatalf("the long notice should expire at +11s, removed %d", removed)
	}
	if _, ok := s.Peek(); ok {
		t.Fatal("stack must be empty after full expiry")
	}

	// Expiry is inclusive at the exact TTL boundary.
	var d NoticeStack
	exact := d.Push(SeverityWarn, "boundary", time.Second, base)
	if exact.Expired(base.Add(time.Second - time.Nanosecond)) {
		t.Fatal("notice must still be live just before its TTL")
	}
	if !exact.Expired(base.Add(time.Second)) {
		t.Fatal("notice must be expired exactly at its TTL")
	}

	// A zero TTL falls back to DefaultNoticeTTL.
	var def NoticeStack
	n := def.ReportInfo("default ttl", 0, base)
	if n.TTL != DefaultNoticeTTL {
		t.Fatalf("default TTL = %v, want %v", n.TTL, DefaultNoticeTTL)
	}
	if removed := def.Prune(base.Add(DefaultNoticeTTL)); removed != 1 {
		t.Fatalf("default-TTL notice should expire, removed %d", removed)
	}
}

// TestNoticeStackClearRetirement checks that retirement by sequence never
// lets a stale clear retire a newer notice, and that seqs stay monotonic.
func TestNoticeStackClearRetirement(t *testing.T) {
	base := time.Unix(3_000, 0)
	var s NoticeStack

	old := s.ReportError("old error", time.Hour, base)
	newer := s.ReportInfo("new info", time.Hour, base)

	// The error currently wins on severity; retiring it exposes the newer one.
	if got, _ := s.Peek(); got.Seq != old.Seq {
		t.Fatal("old error should be displayed before retirement")
	}
	if !s.Clear(old.Seq) {
		t.Fatal("Clear of a live notice must report removal")
	}
	if got, ok := s.Peek(); !ok || got.Seq != newer.Seq {
		t.Fatalf("retiring the old notice must reveal the newer one: %+v, %v", got, ok)
	}

	// A repeated / stale clear for the old seq is a no-op and cannot take out
	// the newer notice.
	if s.Clear(old.Seq) {
		t.Fatal("clearing an already-retired seq must be a no-op")
	}
	if got, ok := s.Peek(); !ok || got.Seq != newer.Seq {
		t.Fatalf("stale clear retired a newer notice: %+v, %v", got, ok)
	}
	if s.Clear(9999) {
		t.Fatal("clearing an unknown seq must be a no-op")
	}

	// Sequence numbers are monotonic across clears.
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

// TestNoticeStackConcurrentAccess exercises Push/Peek/Clear/Prune/Current from
// many goroutines; with a mutex all operations stay safe and seqs are unique.
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

	// Concurrent readers/retirers using a zero time so Prune removes nothing.
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

	// Concurrent Clear calls must retire each notice exactly once without
	// corrupting the slice.
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

// TestNoticeStackCJKText verifies multilingual text is stored and selected
// verbatim; clipping is a renderer concern, not the manager's.
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
