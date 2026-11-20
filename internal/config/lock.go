package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// lockFilePath 返回配置文件 path 对应的跨进程锁文件路径，形如
// "<清理后的 path>.lock"。它与 security 包的 grants.json.lock
// 采用相同的 sidecar 命名，便于运维识别。
//
// 这是纯函数：不触碰文件系统，只做路径清理与拼接。filepath.Clean
// 会消解 "."、".." 和重复分隔符；空白路径被视为非法并返回空字符串，
// 以免生成会落到当前目录的意外文件名。CJK 路径按 UTF-8 字节原样
// 保留，不做 ASCII 转写，因此对中英文路径都稳定。
func lockFilePath(configPath string) string {
	trimmed := strings.TrimSpace(configPath)
	if trimmed == "" {
		return ""
	}
	return filepath.Clean(trimmed) + ".lock"
}

// withConfigLock 在 configPath 的 sidecar 锁文件保护下运行 fn。
// 锁是操作系统级的字节范围锁：拥有它的进程退出（包括崩溃）时内核
// 自动释放，因此不会留下需要清理的陈旧锁文件。锁文件按需以 0600
// 权限创建在与配置文件相同的目录；调用方在 fn 内执行读-改-写序列，
// 即可避免多个进程并发写配置时互相覆盖。
//
// configPath 非法（清理后为空）或无法创建锁文件时返回错误且不执行 fn。
func withConfigLock(configPath string, fn func() error) error {
	lockPath := lockFilePath(configPath)
	if lockPath == "" {
		return fmt.Errorf("config: empty config path for lock")
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return fmt.Errorf("config: create lock dir: %w", err)
	}
	lk, err := configAcquireLock(lockPath)
	if err != nil {
		return err
	}
	defer lk.Release()
	return fn()
}
