package tui

import (
	"sync"
	"time"
)

// DefaultNoticeTTL is the lifetime applied to a notice pushed without an
// explicit TTL. It matches Crush's DefaultStatusTTL so an omitted TTL cannot
// pin a notice on the status line forever.
const DefaultNoticeTTL = 5 * time.Second

// Severity classifies a status notice. Its numeric value doubles as the
// display priority: when several live notices overlap, the one with the
// highest severity wins. The eight levels mirror Crush's eight InfoType
// categories (internal/ui/util/util.go:28-39) but are deliberately ordered by
// urgency, so errors and warnings outrank purely informational mode banners.
type Severity uint8

const (
	SeverityInfo Severity = iota
	SeveritySuccess
	SeverityUpdate
	SeverityPlan
	SeverityYolo
	SeveritySysadmin
	SeverityWarn
	SeverityError
)

var severityNames = [...]string{
	"info", "success", "update", "plan", "yolo", "sysadmin", "warn", "error",
}

func (s Severity) String() string {
	if int(s) < len(severityNames) {
		return severityNames[s]
	}
	return "unknown"
}

// Notice is a single status notification. Seq is assigned by NoticeStack.Push
// and is the identity Clear uses to retire exactly this notice; Text is kept
// verbatim (CJK included) and is clipped only at render time.
type Notice struct {
	Severity Severity
	Text     string
	Seq      uint64
	TTL      time.Duration
	At       time.Time
}

// Expired reports whether the notice has outlived its TTL at time now. A
// non-positive TTL means the notice never expires on its own.
func (n Notice) Expired(now time.Time) bool {
	return n.TTL > 0 && !now.Before(n.At.Add(n.TTL))
}

// NoticeStack is a concurrency-safe collection of live status notices. It is
// the pure-logic replacement for Model's single status string with a fixed
// 6s expiry: notices are ranked by severity, expire on their own TTL, and are
// retired by their unique sequence number so a stale TTL callback can never
// clear a newer notice. The zero value is ready to use.
type NoticeStack struct {
	mu      sync.Mutex
	nextSeq uint64
	notices []Notice
}

// Push stores a notice, returning it with an assigned, monotonically
// increasing Seq and the supplied timestamp at. A non-positive ttl is
// replaced by DefaultNoticeTTL.
func (s *NoticeStack) Push(sev Severity, text string, ttl time.Duration, at time.Time) Notice {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextSeq++
	if ttl <= 0 {
		ttl = DefaultNoticeTTL
	}
	n := Notice{Severity: sev, Text: text, Seq: s.nextSeq, TTL: ttl, At: at}
	s.notices = append(s.notices, n)
	return n
}

// ReportInfo, ReportWarn and ReportError mirror Crush's ReportInfo /
// ReportWarn / ReportError helpers: they push a notice of the corresponding
// severity and return it so the caller can schedule Clear(n.Seq) after its
// TTL.
func (s *NoticeStack) ReportInfo(text string, ttl time.Duration, at time.Time) Notice {
	return s.Push(SeverityInfo, text, ttl, at)
}

func (s *NoticeStack) ReportWarn(text string, ttl time.Duration, at time.Time) Notice {
	return s.Push(SeverityWarn, text, ttl, at)
}

func (s *NoticeStack) ReportError(text string, ttl time.Duration, at time.Time) Notice {
	return s.Push(SeverityError, text, ttl, at)
}

// Peek returns the highest-severity stored notice without removing it. Ties
// are broken by the newest sequence number, so among equally severe notices
// the newer one is shown. Peek does not check TTL; call Prune (or Current)
// first when expiry matters. The bool is false when the stack is empty.
func (s *NoticeStack) Peek() (Notice, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.notices) == 0 {
		return Notice{}, false
	}
	best := s.notices[0]
	for _, n := range s.notices[1:] {
		if n.Severity > best.Severity || (n.Severity == best.Severity && n.Seq > best.Seq) {
			best = n
		}
	}
	return best, true
}

// Prune drops notices whose TTL has elapsed at now and returns the number
// removed. Expired notices are gone for good and can no longer be displayed.
func (s *NoticeStack) Prune(now time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.notices[:0]
	for _, n := range s.notices {
		if n.Expired(now) {
			continue
		}
		kept = append(kept, n)
	}
	removed := len(s.notices) - len(kept)
	s.notices = kept
	return removed
}

// Current prunes expired notices at now and returns the notice that should be
// displayed on the status line, if any.
func (s *NoticeStack) Current(now time.Time) (Notice, bool) {
	s.Prune(now)
	return s.Peek()
}

// Clear retires the notice identified by seq. Because Seq is unique and
// monotonic, a stale clear (an old notice's TTL callback) can only retire
// that old notice and can never clear a newer one; this is the stack's
// equivalent of Crush's ClearStatusMsg{Seq} retirement. It reports whether a
// notice was actually removed.
func (s *NoticeStack) Clear(seq uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.notices[:0]
	removed := false
	for _, n := range s.notices {
		if n.Seq == seq {
			removed = true
			continue
		}
		kept = append(kept, n)
	}
	s.notices = kept
	return removed
}

// Len returns the number of stored (not yet expired or retired) notices.
func (s *NoticeStack) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.notices)
}

// Seq returns the sequence number assigned by the most recent Push.
func (s *NoticeStack) Seq() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nextSeq
}
