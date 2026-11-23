package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ErrTurnLeaseHeld 表示同一会话已有另一个进程持有活租约，当前 turn 必须拒绝，
// 以免两个进程各自压缩、追加并交错产出记录。
var ErrTurnLeaseHeld = errors.New("session: turn lease held by another process")

const (
	turnLeaseEnv = "ARGENT_TURN_LEASE"

	// 标记 60s 未刷新即视为陈旧；活跃租约每 TTL/3≈20s 续期，不受影响。
	// 本机 TTL 从 24h 降到 60s：标记内容只有主机名、无进程启动时间，Windows
	// PID 可被快速复用——若崩溃后 PID 被新进程占用，OpenProcess 判活会误把
	// 陈旧标记当成活跃，24h TTL 会让下次运行被拒一整天。60s 把误判上限收回
	// 到一分钟，代价仅是挂起超 60s 的进程不再持有租约（本就应让出）。
	localTurnLeaseTTL   = 60 * time.Second
	foreignTurnLeaseTTL = 60 * time.Second
	turnLeaseRefreshDiv = 3
	maxTurnLeaseHostLen = 256
)

// TurnLease 是跨进程的 turn 租约。标记文件名为 <sessionID>.<pid>，内容为主机名，
// 每 TTL/3 刷新一次 mtime；释放时删除标记。它通过 TTL + PID 存活双判定清理陈旧标记。
type TurnLease struct {
	dir       string
	sessionID string
	path      string
	host      string
	cancel    context.CancelFunc
	noop      bool
}

// TurnLeaseEnabled 报告租约是否启用。默认开启；仅显式设置为 "0" 时紧急关闭，
// 给不希望产生 sidecar 标记的场景留出逃生阀（用户零选择）。
// 它读取进程环境，供 library 直接调用者使用；Execute 请用 TurnLeaseEnabledFrom
// 传入本次运行的 per-run env，避免多表面（--serve/rpc/TUI）各自的环境被忽略。
func TurnLeaseEnabled() bool {
	return TurnLeaseEnabledFrom(os.Getenv)
}

// TurnLeaseEnabledFrom 是 TurnLeaseEnabled 的 per-run env 变体。
// env 为 nil 时按进程环境回退，保持零值兼容。
func TurnLeaseEnabledFrom(env func(string) string) bool {
	if env == nil {
		env = os.Getenv
	}
	return strings.TrimSpace(env(turnLeaseEnv)) != "0"
}

// AcquireTurnLease 尝试为 dir 中的 sessionID 获取 turn 租约。
// 发现活标记时返回 ErrTurnLeaseHeld；陈旧标记（PID 已死或超 TTL）会被清理后重试。
func AcquireTurnLease(dir, sessionID string) (*TurnLease, error) {
	if !TurnLeaseEnabled() {
		return &TurnLease{noop: true}, nil
	}
	trimmed := strings.TrimSpace(sessionID)
	if dir == "" || trimmed == "" {
		return nil, fmt.Errorf("session: turn lease needs a directory and session id")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("session: create turn lease dir: %w", err)
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	if len(host) > maxTurnLeaseHostLen {
		host = host[:maxTurnLeaseHostLen]
	}
	lease := &TurnLease{dir: dir, sessionID: trimmed, host: host}
	if err := lease.clearForeignMarkers(); err != nil {
		return nil, err
	}
	lease.path = filepath.Join(dir, trimmed+"."+strconv.Itoa(os.Getpid()))
	f, err := os.OpenFile(lease.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return nil, ErrTurnLeaseHeld
		}
		return nil, fmt.Errorf("session: create turn lease marker: %w", err)
	}
	if _, err := f.WriteString(host); err != nil {
		f.Close()
		os.Remove(lease.path)
		return nil, fmt.Errorf("session: write turn lease marker: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(lease.path)
		return nil, fmt.Errorf("session: close turn lease marker: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	lease.cancel = cancel
	go lease.refreshLoop(ctx, foreignTurnLeaseTTL/turnLeaseRefreshDiv)
	return lease, nil
}

// clearForeignMarkers 扫描同会话的标记文件，删除陈旧标记；发现活标记时返回 ErrTurnLeaseHeld。
func (l *TurnLease) clearForeignMarkers() error {
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return fmt.Errorf("session: read turn lease dir: %w", err)
	}
	prefix := l.sessionID + "."
	now := time.Now()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, prefix) {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimPrefix(name, prefix))
		if err != nil || pid <= 0 {
			// 无法解析的标记不属于任何活进程，直接清理。
			_ = os.Remove(filepath.Join(l.dir, name))
			continue
		}
		markerPath := filepath.Join(l.dir, name)
		info, err := os.Lstat(markerPath)
		if err != nil {
			continue
		}
		markerHost := readLeaseHost(markerPath)
		local := markerHost == l.host
		ttl := foreignTurnLeaseTTL
		if local {
			ttl = localTurnLeaseTTL
		}
		stale := !leasePIDAlive(pid) || now.Sub(info.ModTime()) > ttl
		if stale {
			_ = os.Remove(markerPath)
			continue
		}
		return ErrTurnLeaseHeld
	}
	return nil
}

// readLeaseHost 读取标记内容（主机名）；读取失败时按异机处理（较短 TTL）。
func readLeaseHost(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

// refreshLoop 每 ttl/3 刷新一次 mtime，使长 turn 的标记不会因超时被别的进程清理。
func (l *TurnLease) refreshLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			l.Refresh()
		}
	}
}

// Refresh 续期租约（刷新标记 mtime）。
func (l *TurnLease) Refresh() {
	if l == nil || l.noop || l.path == "" {
		return
	}
	now := time.Now()
	_ = os.Chtimes(l.path, now, now)
}

// Release 释放租约：停止刷新并删除自己的标记。重复调用安全（幂等）。
func (l *TurnLease) Release() {
	if l == nil || l.noop {
		return
	}
	if l.cancel != nil {
		l.cancel()
		l.cancel = nil
	}
	if l.path != "" {
		_ = os.Remove(l.path)
		l.path = ""
	}
}
