// 本文件实现轻量级的交互式 exec 会话注册表。
//
// 它与同包的后台作业注册表互补：后台作业把子进程分离到日志文件里，
// 适合「启动后不必再理」的长任务；这里的会话保留 stdin 写端与有界输出环，
// 让 REPL、dev server、watch 模式等需要多轮交互的进程真正可用。
//
// 设计要点：
//   - 进程实现由调用方以 SessionHandle/SessionRunner 注入，jobs 只管注册表、
//     输出环、空闲回收与 yield 窗口，因此这里保持只用标准库；
//   - 默认空闲 TTL 与输出上限固定，opt-in 的总开关在调用方（internal/bash），
//     关闭时本文件的代码不会被触达；
//   - 所有方法都可从多个 goroutine 安全调用，写 stdin 在会话粒度串行化。
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// 交互会话的容量与默认时间常数。数值参考 codex unified_exec：
// 单次 stdin 写入 8000 字节、输出封顶 1MiB、启动会话默认 yield 10s、
// 后续 write_stdin 默认 yield 250ms。
const (
	MaxSessionStdinBytes   = 8000
	MaxSessionOutputBytes  = 1 << 20
	DefaultExecYield       = 10 * time.Second
	DefaultWriteStdinYield = 250 * time.Millisecond

	defaultSessionIdleTTL    = 5 * time.Minute
	defaultSessionReapPeriod = 30 * time.Second
	sessionPollInterval      = 10 * time.Millisecond
	sessionOutputQuietPeriod = 40 * time.Millisecond
	sessionIDBytes           = 12
)

var (
	// ErrSessionNotFound 表示 session_id 在注册表中不存在。
	ErrSessionNotFound = errors.New("jobs: exec session not found")
	// ErrSessionClosed 表示会话已被关闭或回收，不可继续写入。
	ErrSessionClosed = errors.New("jobs: exec session closed")
)

// SessionHandle 是调用方注入的会话进程句柄。jobs 只通过这些方法与进程交互，
// 因此本包不需要知道 ConPTY、进程组或具体的 spawn 细节。
type SessionHandle interface {
	// WriteStdin 向进程 stdin 写入数据并返回实际写入字节数。
	WriteStdin(p []byte) (int, error)
	// Kill 终止进程及其后代，必须幂等。
	Kill()
	// Done 在进程退出后被关闭。
	Done() <-chan struct{}
	// PID 返回进程 id，仅用于展示与诊断。
	PID() int
}

// SessionRunner 启动一条会话进程，把它的 stdout/stderr 汇总写入 output。
// output 由注册表提供，是一个有界环形缓冲。
type SessionRunner interface {
	Start(output io.Writer) (SessionHandle, error)
}

// SessionStart 描述如何启动一条交互会话。
type SessionStart struct {
	// ID 非空时作为会话 id；为空则由注册表生成。
	ID string
	// Command 仅用于展示与诊断。
	Command string
	// Dir 是子进程工作目录；空表示继承。
	Dir string
	// TTY 报告会话是否附着伪终端。
	TTY bool
	// Yield 是等待首批输出的最长时间；零值取 DefaultExecYield。
	Yield time.Duration
	// Stdin 是启动后立即写入的输入，长度受 MaxSessionStdinBytes 限制。
	Stdin []byte
	// Runner 必填，负责真正的进程启动。
	Runner SessionRunner
}

// SessionInfo 是会话在某一时刻的快照。
type SessionInfo struct {
	ID        string
	Command   string
	PID       int
	TTY       bool
	StartedAt time.Time
	Running   bool
}

// SessionRegistry 在内存中维护交互会话并回收空闲者。
// 与同包的 Registry 不同，它不落盘：交互进程的 stdin/stdout 管道无法跨进程恢复。
type SessionRegistry struct {
	mu       sync.Mutex
	sessions map[string]*liveSession
	next     uint64

	idleTTL time.Duration
	ctx     context.Context
	cancel  context.CancelFunc
	ticker  *time.Ticker
}

// liveSession 是一条在册会话的可变状态。
type liveSession struct {
	info     SessionInfo
	ring     *sessionRing
	handle   SessionHandle
	lastUsed atomic.Int64
	dead     atomic.Bool
	// cursor 是调用方已消费的输出绝对位置，由 writeMu 保护。
	cursor  uint64
	writeMu sync.Mutex
	// killOnce 保证 Kill 在回收与显式关闭并发时只执行一次。
	killOnce sync.Once
}

// NewSessionRegistry 构造一个注册表并启动空闲回收器。
// 生命周期用独立的 context 持有，工具调用的取消不会拆掉整个会话池。
func NewSessionRegistry() *SessionRegistry {
	ctx, cancel := context.WithCancel(context.Background())
	r := &SessionRegistry{
		sessions: make(map[string]*liveSession),
		idleTTL:  defaultSessionIdleTTL,
		ctx:      ctx,
		cancel:   cancel,
		ticker:   time.NewTicker(defaultSessionReapPeriod),
	}
	go r.reapLoop()
	return r
}

// Start 启动一条新会话，等待首批输出后返回会话快照与输出文本。
func (r *SessionRegistry) Start(s SessionStart) (SessionInfo, string, error) {
	if s.Runner == nil {
		return SessionInfo{}, "", errors.New("jobs: nil exec session runner")
	}
	if len(s.Stdin) > MaxSessionStdinBytes {
		return SessionInfo{}, "", fmt.Errorf("jobs: stdin exceeds %d bytes", MaxSessionStdinBytes)
	}

	id := s.ID
	if id == "" {
		id = r.allocateID()
	}
	ring := &sessionRing{}
	handle, err := s.Runner.Start(ring)
	if err != nil {
		return SessionInfo{}, "", fmt.Errorf("jobs: start exec session: %w", err)
	}

	ls := &liveSession{
		info: SessionInfo{
			ID:        id,
			Command:   s.Command,
			PID:       handle.PID(),
			TTY:       s.TTY,
			StartedAt: time.Now().UTC(),
			Running:   true,
		},
		ring:   ring,
		handle: handle,
	}
	ls.lastUsed.Store(time.Now().UnixMilli())

	r.mu.Lock()
	if _, exists := r.sessions[id]; exists {
		r.mu.Unlock()
		handle.Kill()
		return SessionInfo{}, "", fmt.Errorf("jobs: exec session %q already exists", id)
	}
	r.sessions[id] = ls
	r.mu.Unlock()

	if len(s.Stdin) > 0 {
		if _, werr := handle.WriteStdin(s.Stdin); werr != nil {
			r.mu.Lock()
			delete(r.sessions, id)
			r.mu.Unlock()
			ls.dead.Store(true)
			ls.killOnce.Do(handle.Kill)
			return SessionInfo{}, "", fmt.Errorf("jobs: initial stdin write: %w", werr)
		}
		ls.lastUsed.Store(time.Now().UnixMilli())
	}

	out := ls.collect(s.Yield)
	return r.snapshot(ls), out, nil
}

// WriteStdin 向已有会话写入输入，收集 yield 窗口内的输出。
// running 报告进程在收集结束时是否仍在运行，便于调用方附加退出提示。
func (r *SessionRegistry) WriteStdin(id string, input []byte, yield time.Duration) (out string, running bool, err error) {
	ls := r.lookup(id)
	if ls == nil {
		return "", false, ErrSessionNotFound
	}
	if ls.dead.Load() {
		return "", false, ErrSessionClosed
	}
	if len(input) > MaxSessionStdinBytes {
		return "", true, fmt.Errorf("jobs: stdin exceeds %d bytes", MaxSessionStdinBytes)
	}

	ls.writeMu.Lock()
	defer ls.writeMu.Unlock()
	if ls.dead.Load() {
		return "", false, ErrSessionClosed
	}
	if len(input) > 0 {
		n, werr := ls.handle.WriteStdin(input)
		if werr != nil {
			return "", false, fmt.Errorf("jobs: write stdin: %w", werr)
		}
		if n < len(input) {
			return "", false, fmt.Errorf("jobs: short stdin write: %d/%d bytes", n, len(input))
		}
		ls.lastUsed.Store(time.Now().UnixMilli())
	}
	out = ls.collect(yield)
	return out, r.snapshot(ls).Running, nil
}

// Close 终止并摘除一条会话，幂等：对已关闭会话返回 ErrSessionNotFound。
func (r *SessionRegistry) Close(id string) error {
	r.mu.Lock()
	ls := r.sessions[id]
	delete(r.sessions, id)
	r.mu.Unlock()
	if ls == nil {
		return ErrSessionNotFound
	}
	ls.dead.Store(true)
	ls.killOnce.Do(ls.handle.Kill)
	return nil
}

// closeAll 终止注册表内全部会话，供空闲回收器与同包测试使用。
func (r *SessionRegistry) closeAll() {
	r.mu.Lock()
	all := make([]*liveSession, 0, len(r.sessions))
	for _, ls := range r.sessions {
		all = append(all, ls)
	}
	r.sessions = make(map[string]*liveSession)
	r.mu.Unlock()
	for _, ls := range all {
		ls.dead.Store(true)
		ls.killOnce.Do(ls.handle.Kill)
	}
}

func (r *SessionRegistry) lookup(id string) *liveSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessions[id]
}

func (r *SessionRegistry) snapshot(ls *liveSession) SessionInfo {
	info := ls.info
	select {
	case <-ls.handle.Done():
		info.Running = false
	default:
	}
	return info
}

func (r *SessionRegistry) allocateID() string {
	r.mu.Lock()
	r.next++
	n := r.next
	r.mu.Unlock()
	var b [sessionIDBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		// 随机源失败时退化为序号 id：仍唯一，不影响正确性。
		return fmt.Sprintf("exec-%d", n)
	}
	return "exec-" + hex.EncodeToString(b[:])
}

// collect 在 yield 窗口内收集新输出。输出停止增长一小段时间即提前返回，
// 使交互式 REPL 的每次写入都能立刻拿到响应，而不必等满整个窗口。
func (s *liveSession) collect(yield time.Duration) string {
	if yield <= 0 {
		yield = DefaultExecYield
	}
	deadline := time.Now().Add(yield)
	lastGrowth := time.Now()
	var out []byte
	for {
		data, next := s.ring.snapshotFrom(s.cursor)
		s.cursor = next
		if len(data) > 0 {
			out = append(out, data...)
			lastGrowth = time.Now()
		}

		select {
		case <-s.handle.Done():
			// 进程刚退出时把最后一段输出排空。
			data, next = s.ring.snapshotFrom(s.cursor)
			s.cursor = next
			out = append(out, data...)
			return string(out)
		default:
		}
		if len(out) > 0 && time.Since(lastGrowth) >= sessionOutputQuietPeriod {
			return string(out)
		}
		if !time.Now().Before(deadline) {
			return string(out)
		}
		time.Sleep(sessionPollInterval)
	}
}

func (r *SessionRegistry) reapLoop() {
	for {
		select {
		case <-r.ticker.C:
			r.reapIdle()
		case <-r.ctx.Done():
			return
		}
	}
}

// reapIdle 回收空闲超时与已退出的会话。
func (r *SessionRegistry) reapIdle() {
	now := time.Now()
	r.mu.Lock()
	var stale []*liveSession
	for id, ls := range r.sessions {
		exited := false
		select {
		case <-ls.handle.Done():
			exited = true
		default:
		}
		if exited || now.Sub(time.UnixMilli(ls.lastUsed.Load())) > r.idleTTL {
			ls.dead.Store(true)
			stale = append(stale, ls)
			delete(r.sessions, id)
		}
	}
	r.mu.Unlock()
	for _, ls := range stale {
		ls.killOnce.Do(ls.handle.Kill)
	}
}

// sessionRing 保存最近 MaxSessionOutputBytes 字节输出，并用绝对位置计数，
// 让调用方可以在环形缓冲覆盖旧字节后仍正确判断"自上次读取以来的新输出"。
type sessionRing struct {
	mu    sync.Mutex
	buf   []byte
	total uint64
}

func (r *sessionRing) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := len(p)
	r.total += uint64(n)
	r.buf = append(r.buf, p...)
	if len(r.buf) > MaxSessionOutputBytes {
		excess := len(r.buf) - MaxSessionOutputBytes
		copy(r.buf, r.buf[excess:])
		r.buf = r.buf[:MaxSessionOutputBytes]
	}
	return n, nil
}

// snapshotFrom 返回从绝对位置 pos 起的全部可用字节，以及新的绝对位置。
// pos 落在已被覆盖的区间时从缓冲头部开始，避免读取到陈旧内容。
func (r *sessionRing) snapshotFrom(pos uint64) (string, uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	base := r.total - uint64(len(r.buf))
	if pos < base {
		pos = base
	}
	if pos > r.total {
		pos = r.total
	}
	return string(r.buf[int(pos-base):]), r.total
}
