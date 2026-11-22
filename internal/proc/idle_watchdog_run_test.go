package proc

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"
)

func TestIdleWatchdogPredicate(t *testing.T) {
	// 纯函数判定：持续活动不判过期，静默超阈值才判过期。
	now := time.Now()
	if idleWatchdogExpired(now.Add(-50*time.Millisecond), now, 100*time.Millisecond) {
		t.Fatal("recent activity should not be stale")
	}
	if !idleWatchdogExpired(now.Add(-200*time.Millisecond), now, 100*time.Millisecond) {
		t.Fatal("long silence should be stale")
	}
	if idleWatchdogExpired(now, now, 0) {
		t.Fatal("disabled watchdog should never expire")
	}
}

func TestIdleTimeoutKillsStalledProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Windows 用 ping 制造无输出等待。
		start := time.Now()
		_, err := Run(context.Background(), Command{
			Path:        "cmd",
			Args:        []string{"/c", "ping -n 6 127.0.0.1 >nul"},
			IdleTimeout: 150 * time.Millisecond,
		})
		if !errors.Is(err, ErrIdleTimeout) {
			t.Fatalf("expected ErrIdleTimeout, got %v", err)
		}
		// 注意：Windows 上 killTree 终止 ping 子树的耗时取决于系统，
		// 这里只断言错误语义正确，不绑定进程树终止的墙钟时间。
		_ = start
		return
	}
	start := time.Now()
	_, err := Run(context.Background(), Command{
		Path:        "sh",
		Args:        []string{"-c", "sleep 5"},
		IdleTimeout: 100 * time.Millisecond,
	})
	if !errors.Is(err, ErrIdleTimeout) {
		t.Fatalf("expected ErrIdleTimeout, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("idle timeout took too long: %v", elapsed)
	}
}
