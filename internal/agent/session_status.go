package agent

import "sync"

// Status 表示一次会话执行所处的状态。
type Status string

const (
	// StatusIdle 表示会话空闲，也是未知/已删除会话的缺省状态。
	StatusIdle Status = "idle"
	// StatusPlanning 表示会话正在规划。
	StatusPlanning Status = "planning"
	// StatusWorking 表示会话正在执行工具或生成回复。
	StatusWorking Status = "working"
	// StatusDone 表示会话已正常结束。
	StatusDone Status = "done"
	// StatusError 表示会话因错误结束。
	StatusError Status = "error"
)

// SessionStatusRegistry 按会话 id 记录执行状态。
//
// 并发子 agent 各自持有独立的会话 id，因此状态互不串线。零值即可使用；
// 内部 map 在首次 Set 时惰性初始化。所有方法均可被多个 goroutine 并发调用。
type SessionStatusRegistry struct {
	mu       sync.RWMutex
	statuses map[string]Status
}

// Set 记录 sessionID 的状态。空 id 同样会被记录，不做特殊过滤。
func (r *SessionStatusRegistry) Set(sessionID string, status Status) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.statuses == nil {
		r.statuses = make(map[string]Status)
	}
	r.statuses[sessionID] = status
}

// Get 返回 sessionID 的状态。未记录的会话（含已删除）返回 StatusIdle。
func (r *SessionStatusRegistry) Get(sessionID string) Status {
	r.mu.RLock()
	defer r.mu.RUnlock()
	status, ok := r.statuses[sessionID]
	if !ok {
		return StatusIdle
	}
	return status
}

// Delete 移除 sessionID 的记录；之后 Get 该会话将返回 StatusIdle。
func (r *SessionStatusRegistry) Delete(sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.statuses, sessionID)
}

// Snapshot 返回当前所有状态的副本。
//
// 返回值是新分配的 map，调用方可以随意修改，不会影响注册表内部状态。
// 即使尚未写入任何记录，也会返回一个非 nil 的空 map。
func (r *SessionStatusRegistry) Snapshot() map[string]Status {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]Status, len(r.statuses))
	for id, status := range r.statuses {
		out[id] = status
	}
	return out
}
