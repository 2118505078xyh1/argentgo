package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// appendLockExt 是跨进程追加锁的 sidecar 后缀。它与会话文件放在同一目录，
// 命名与 config 的 ".lock"、security 的 "grants.json.lock" 保持同一家族风格，
// 便于运维识别；同时不会命中 Store.List 的 ".jsonl" 过滤条件。
const appendLockExt = ".append.lock"

// appendLockFilePath 返回会话文件 path 对应的跨进程锁文件路径。
// 这是纯函数：只做清理与拼接，不触碰文件系统。空白路径返回空字符串，
// 以免生成落到当前目录的意外文件名。
func appendLockFilePath(sessionPath string) string {
	trimmed := strings.TrimSpace(sessionPath)
	if trimmed == "" {
		return ""
	}
	return filepath.Clean(trimmed) + appendLockExt
}

// withAppendLock 在 lockPath 这个 sidecar 锁的保护下运行 fn。
// lockPath 由 appendLockFilePath 从会话路径派生，调用方负责派生并缓存，
// 这里不再重复加工，避免出现 ".append.lock.append.lock" 这样的双重后缀。
// 锁是操作系统级的文件锁：持有它的进程退出（包括崩溃）时内核/系统自动释放，
// 因此不会留下需要回收的陈旧锁。调用方在 fn 内执行"生成 id → 落盘"序列，
// 即可避免多个进程基于同一份进程内快照写出相同的 id/parent。
//
// 在无跨进程锁能力的平台上，appendAcquireLock 返回空操作锁，fn 仍会执行，
// 进程内的串行化由调用方持有的 s.mu 保证（优雅降级）。
func withAppendLock(lockPath string, fn func() error) error {
	if lockPath == "" {
		return fmt.Errorf("session: empty path for append lock")
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return fmt.Errorf("session: create append lock dir: %w", err)
	}
	lk, err := appendAcquireLock(lockPath)
	if err != nil {
		return err
	}
	defer lk.Release()
	return fn()
}
