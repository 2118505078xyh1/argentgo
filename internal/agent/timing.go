package agent

import "time"

// timeSource 是事件计时所依赖的墙上时钟。它是变量，便于测试注入确定性时钟；
// 生产环境始终使用 time.Now。
var timeSource = time.Now

// eventTiming 是一个可观察步骤（工具调用或推理窗口）的墙上时钟区间。
// StartedAtMS 和 FinishedAtMS 是绝对的 Unix 纪元毫秒：与相对的 DurationMS 不同，
// 它们在会话重载后仍然有意义，这正是前端重建真实思考/工具耗时所需要的，
// 而不必从第一帧的时钟凭空编造一个。
type eventTiming struct {
	StartedAtMS  int64
	FinishedAtMS int64
	Elapsed      time.Duration
}

// unixMillis 把 t 呈现为 Unix 纪元毫秒。零值时间呈现为 0，
// 这样没有计时的事件会保持新增的 omitempty 字段缺省，使现有 JSON 编码逐字节不变。
func unixMillis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// bracketTiming 从起止时间对推导线上计时。不完整或倒置的时间对会产生零值，
// 因此当窗口缺少一端时，调用方绝不会编造时间戳或时长。
func bracketTiming(start, finish time.Time) eventTiming {
	if start.IsZero() || finish.IsZero() {
		return eventTiming{}
	}
	t := eventTiming{
		StartedAtMS:  unixMillis(start),
		FinishedAtMS: unixMillis(finish),
	}
	if !finish.Before(start) {
		t.Elapsed = finish.Sub(start)
	}
	return t
}

// startTool 记录调用 id 的墙上时钟起点。并行 worker 操作不同的 id，因此该映射受锁保护。
// nil 映射会延迟创建，使手工构造的 runState（例如测试中）也安全。
func (s *runState) startTool(id string, at time.Time) {
	s.timingMu.Lock()
	defer s.timingMu.Unlock()
	if s.toolTimes == nil {
		s.toolTimes = make(map[string]time.Time)
	}
	s.toolTimes[id] = at
}

// finishTool 消费为 id 记录的起点，并用 at 与之构成区间。
// 未知 id（被阻断、已取消或手工构造的状态）产生零值：不会编造时间戳，omitempty 字段保持缺省。
func (s *runState) finishTool(id string, at time.Time) eventTiming {
	s.timingMu.Lock()
	start, ok := s.toolTimes[id]
	if ok {
		delete(s.toolTimes, id)
	}
	s.timingMu.Unlock()
	if !ok {
		return eventTiming{}
	}
	return bracketTiming(start, at)
}

// startReasoning 标记产生推理块的请求的开始时刻。推理在循环 goroutine 上发出，
// 但与 toolTimes 共享 timingMu 可使状态相对于流式进度回调保持无竞态。
func (s *runState) startReasoning(at time.Time) {
	s.timingMu.Lock()
	s.reasoningStart = at
	s.timingMu.Unlock()
}

// finishReasoning 关闭当前推理窗口并返回其墙上时钟区间。
// 没有事先调用 start 就调用它是安全的空操作。
func (s *runState) finishReasoning(at time.Time) eventTiming {
	s.timingMu.Lock()
	start := s.reasoningStart
	s.reasoningStart = time.Time{}
	s.timingMu.Unlock()
	return bracketTiming(start, at)
}
