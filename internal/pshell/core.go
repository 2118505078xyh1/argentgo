package pshell

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

	"github.com/2118505078xyh1/argentgo/internal/proc"
)

const (
	defaultCommandTimeout = 30 * time.Second // 单命令超时：超时即销毁会话，下条命令重建
	defaultIdleTTL        = 5 * time.Minute  // 会话空闲超过该时长被后台回收
	defaultReapInterval   = 30 * time.Second // 空闲扫描周期
	nonceBytes            = 16               // nonce 随机字节数（32 个 hex 字符）
	readChunk             = 4096             // 每次管道读取的块大小
)

var (
	// ErrTimeout 表示单命令在超时时间内没有读到完整哨兵。
	ErrTimeout = errors.New("pshell: command timed out")
	// ErrClosed 表示 Manager 已关闭，不再接受命令。
	ErrClosed = errors.New("pshell: manager closed")
)

// Manager 管理一组按 workspace 键控的长驻 shell 会话。
// 生命周期用 context.WithCancel(context.Background()) 独立持有，绝不使用
// 工具的 ctx：一次工具调用的取消不应该拆掉整个会话池。
type Manager struct {
	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	sessions map[string]*Session
	spawn    spawn
	// sameProcessFn 是身份校验钩子，默认调用 proc.IsSameProcess。测试注入
	// 它来模拟 PID 身份，而不依赖真实 OS 进程。
	sameProcessFn func(pid int, startedAt int64) bool
	timeout       time.Duration
	idleTTL       time.Duration
	closed        bool

	ticker *time.Ticker
}

// Session 是一条长驻 shell 会话。mu 串行化整次"写命令+读哨兵"，lastUsed 用
// 原子值供后台回收器无锁读取。
type Session struct {
	t         transport
	workspace string
	shellKind string

	mu       sync.Mutex // 串行化整次 round trip，防止并发写入共享管道
	lastUsed atomic.Int64
	dead     atomic.Bool // 不应再被使用（回收器在 m.mu 内提前置位）
	killed   atomic.Bool // 已执行过 kill/Close，保证 shutdown 幂等

	// cwd/venv 是最近一次探针回报的 shell 状态，由 mu 保护。阶段1仅记录，
	// 供后续版本（自动 cd / venv 探测）和同包测试使用。
	cwd  string
	venv string
}

// NewManager 构造 Manager。spawnFn 为 nil 时使用真实 shell。
func NewManager(spawnFn spawn) *Manager {
	if spawnFn == nil {
		spawnFn = defaultSpawn
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		ctx:      ctx,
		cancel:   cancel,
		sessions: make(map[string]*Session),
		spawn:    spawnFn,
		sameProcessFn: func(pid int, startedAt int64) bool {
			return proc.IsSameProcess(pid, startedAt, proc.DefaultStartTimeToleranceMS)
		},
		timeout: defaultCommandTimeout,
		idleTTL: defaultIdleTTL,
		ticker:  time.NewTicker(defaultReapInterval),
	}
	go m.reapLoop()
	return m
}

// Exec 在 workspace 对应的长驻 shell 中执行命令，返回命令 stdout 与退出码。
// 非 0 退出码通过 code 返回，err 仅表示传输/超时/协议错误。
//
// 签名固定为 (ctx, workspace, shellKind, command) —— 由接线代理 B 依赖。
func (m *Manager) Exec(ctx context.Context, workspace, shellKind, command string) (stdout string, code int, err error) {
	for {
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return "", 0, ErrClosed
		}
		s := m.sessions[workspace]
		if s == nil || s.dead.Load() {
			t, e := m.spawn(shellKind, workspace)
			if e != nil {
				m.mu.Unlock()
				return "", 0, fmt.Errorf("pshell: spawn: %w", e)
			}
			s = &Session{t: t, workspace: workspace, shellKind: shellKind}
			s.lastUsed.Store(time.Now().UnixMilli())
			m.sessions[workspace] = s
		}
		m.mu.Unlock()

		s.mu.Lock()
		// 等待 s.mu 期间会话可能被回收器销毁；此时重建后重试。
		if s.dead.Load() {
			s.mu.Unlock()
			continue
		}
		stdout, code, err, discard := m.runLocked(ctx, s, command)
		s.mu.Unlock()

		if discard {
			// 超时/传输损坏：无法保证管道里没有残留写入，销毁会话，下条重建。
			m.shutdown(s)
			return stdout, code, err
		}
		return stdout, code, err
	}
}

// runLocked 在一次 round trip 中写入命令+探针并读取到哨兵。调用方持有 s.mu。
// discard=true 表示该会话已不可信，需要销毁。
func (m *Manager) runLocked(ctx context.Context, s *Session, command string) (stdout string, code int, err error, discard bool) {
	nonce, err := newNonce()
	if err != nil {
		return "", 0, fmt.Errorf("pshell: nonce: %w", err), true
	}

	payload := command + "\n" + probeCommand(s.shellKind, nonce) + "\n"
	if _, err := io.WriteString(s.t, payload); err != nil {
		return "", 0, fmt.Errorf("pshell: write: %w", err), true
	}
	s.lastUsed.Store(time.Now().UnixMilli())

	// 管道没有 TTY，无法发 SIGINT 中断当前命令：超时只能销毁会话。
	timer := time.NewTimer(m.timeout)
	defer timer.Stop()

	type readResult struct {
		data []byte
		err  error
	}
	var buf []byte
	for {
		// 每次读取放在独立 goroutine：超时后 Close 会让阻塞的 Read 返回，
		// 结果由带缓冲 channel 接收，避免 goroutine 泄漏。
		resCh := make(chan readResult, 1)
		go func() {
			var b [readChunk]byte
			n, e := s.t.Read(b[:])
			resCh <- readResult{append([]byte(nil), b[:n]...), e}
		}()

		select {
		case r := <-resCh:
			buf = append(buf, r.data...)
			out, cwd, venv, rc, ok := splitProbe(string(buf), nonce)
			if ok {
				s.cwd, s.venv = cwd, venv
				s.lastUsed.Store(time.Now().UnixMilli())
				return out, rc, nil, false
			}
			if r.err != nil {
				return "", 0, fmt.Errorf("pshell: read: %w", r.err), true
			}
		case <-ctx.Done():
			return "", 0, fmt.Errorf("pshell: %w", ctx.Err()), true
		case <-timer.C:
			return "", 0, ErrTimeout, true
		}
	}
}

// shutdown 标记会话死亡、从池中摘除，并仅在身份仍匹配时杀进程（幂等）。
func (m *Manager) shutdown(s *Session) {
	m.mu.Lock()
	if cur, ok := m.sessions[s.workspace]; ok && cur == s {
		delete(m.sessions, s.workspace)
	}
	s.dead.Store(true)
	m.mu.Unlock()

	// killed 独立于 dead：回收器在持锁时已预先置位 dead，这里仍要完成 kill。
	if !s.killed.CompareAndSwap(false, true) {
		return
	}
	if m.sameProcessFn(s.t.PID(), s.t.StartTime()) {
		_ = s.t.Close()
	}
}

// reapIdle 回收空闲超时的会话。
func (m *Manager) reapIdle() {
	now := time.Now()
	m.mu.Lock()
	var stale []*Session
	for key, s := range m.sessions {
		if now.Sub(time.UnixMilli(s.lastUsed.Load())) > m.idleTTL {
			// 在 m.mu 内置位 dead：拿到会话指针的 Exec 会在 s.mu 处看到它并重试，
			// 避免回收器把一个正在使用的会话从底下杀掉。
			s.dead.Store(true)
			stale = append(stale, s)
			delete(m.sessions, key)
		}
	}
	m.mu.Unlock()
	for _, s := range stale {
		m.shutdown(s)
	}
}

func (m *Manager) reapLoop() {
	for {
		select {
		case <-m.ticker.C:
			m.reapIdle()
		case <-m.ctx.Done():
			return
		}
	}
}

// Close 幂等关闭：取消生命周期、停掉回收器、杀掉所有仍属于本 Manager 的会话。
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.sessions = make(map[string]*Session)
	m.mu.Unlock()

	m.cancel()
	m.ticker.Stop()
	for _, s := range sessions {
		m.shutdown(s)
	}
	return nil
}

// newNonce 生成 16 字节随机 nonce，编码为 32 个小写 hex 字符。
func newNonce() (string, error) {
	var b [nonceBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// probeCommand 构造追加在命令之后的探针命令。先捕获上一条命令的退出码，
// 再打印哨兵行（cwd/venv/退出码）。PowerShell 分支对 $LASTEXITCODE 为空
// （cmdlet 未设置它）的情况回退为 0。
func probeCommand(shellKind, nonce string) string {
	switch shellKind {
	case "powershell", "pwsh":
		pre := "$__ps_rc=$LASTEXITCODE; if ($null -eq $__ps_rc) { $__ps_rc=0 }; "
		return pre + "Write-Output ('" + markerPrefix + nonce + "|' + (Get-Location).Path + '|' + $env:VIRTUAL_ENV + '|' + $__ps_rc)"
	default: // bash / sh / zsh 等 POSIX shell
		return "__ps_rc=$?; printf '" + markerPrefix + nonce + "|%s|%s|%d\\n' \"$PWD\" \"${VIRTUAL_ENV:-}\" \"$__ps_rc\""
	}
}
