package tui

import (
	"sync"
	"time"
)

// DefaultNoticeTTL 是对未显式指定 TTL 的通知施加的生存期。
// 它匹配 Crush 的 DefaultStatusTTL，使省略的 TTL 不会
// 把通知永久钉在状态行上。
const DefaultNoticeTTL = 5 * time.Second

// Severity 对状态通知分类。其数值同时充当
// 显示优先级：当多个存活通知重叠时，严重度
// 最高的那个胜出。八个级别仿照 Crush 的八个 InfoType
// 类别（internal/ui/util/util.go:28-39），但有意按
// 紧迫度排序，使错误和警告优先于纯信息性模式横幅。
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

// Notice 是一条状态通知。Seq 由 NoticeStack.Push 分配，
// 是 Clear 用来精确退休这条通知的标识；Text 逐字保留
// （含 CJK），只在渲染时裁剪。
type Notice struct {
	Severity Severity
	Text     string
	Seq      uint64
	TTL      time.Duration
	At       time.Time
}

// Expired 报告通知在时间 now 是否已超过其 TTL。
// 非正的 TTL 表示通知永不自行过期。
func (n Notice) Expired(now time.Time) bool {
	return n.TTL > 0 && !now.Before(n.At.Add(n.TTL))
}

// NoticeStack 是存活状态通知的并发安全集合。它是
// Model 原来那个带固定 6s 过期的单一状态字符串的纯逻辑替代：
// 通知按严重度排序、按各自 TTL 过期，并
// 由唯一序号退休，使陈旧的 TTL 回调永不
// 清除较新的通知。零值即可使用。
type NoticeStack struct {
	mu      sync.Mutex
	nextSeq uint64
	notices []Notice
}

// Push 存储一条通知，返回它并赋予单调递增的
// Seq 和提供的时间戳 at。非正的 ttl 被
// 替换为 DefaultNoticeTTL。
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

// ReportInfo、ReportWarn 和 ReportError 仿照 Crush 的
// ReportInfo / ReportWarn / ReportError 辅助函数：它们推送相应
// 严重度的通知并返回它，使调用方可在其 TTL 后安排
// Clear(n.Seq)。
func (s *NoticeStack) ReportInfo(text string, ttl time.Duration, at time.Time) Notice {
	return s.Push(SeverityInfo, text, ttl, at)
}

func (s *NoticeStack) ReportWarn(text string, ttl time.Duration, at time.Time) Notice {
	return s.Push(SeverityWarn, text, ttl, at)
}

func (s *NoticeStack) ReportError(text string, ttl time.Duration, at time.Time) Notice {
	return s.Push(SeverityError, text, ttl, at)
}

// Peek 返回最高严重度的已存储通知而不移除它。平局
// 由最新的序号打破，因此在严重度相同的通知中
// 显示较新的那条。Peek 不检查 TTL；当过期重要时先调用
// Prune（或 Current）。栈为空时布尔值为 false。
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

// Prune 丢弃在 now 时 TTL 已过的通知，并返回被
// 移除的数量。过期的通知彻底消失，无法再显示。
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

// Current 在 now 时清理过期通知，并返回应显示在
// 状态行上的通知（如果有）。
func (s *NoticeStack) Current(now time.Time) (Notice, bool) {
	s.Prune(now)
	return s.Peek()
}

// Clear 退休由 seq 标识的通知。因为 Seq 唯一且
// 单调，陈旧的清除（旧通知的 TTL 回调）只能退休
// 那条旧通知，永不会清除较新的通知；这是该栈
// 与 Crush 的 ClearStatusMsg{Seq} 退休机制等价的做法。它报告是否有
// 通知实际被移除。
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

// Len 返回已存储（尚未过期或退休）通知的数量。
func (s *NoticeStack) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.notices)
}

// Seq 返回最近一次 Push 分配的序号。
func (s *NoticeStack) Seq() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nextSeq
}
