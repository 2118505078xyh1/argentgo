package tools

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultSpillRetention 是溢出产出有资格被清理的存留时长。
// 七天能让恢复出的输出在典型调试会话中保留，同时
// 限制长寿命运行进程的磁盘占用。
const DefaultSpillRetention = 7 * 24 * time.Hour

// CleanSpillArtifacts 删除 dir 中 mtime 早于
// maxAge 的规范溢出产出。它刻意保守，以便可以在
// 会话启动时无条件调用而不危及用户数据：
//
//   - 只考虑文件名完全符合产出契约的文件，因此
//     恰好位于该目录中的无关文件绝不会被触碰；
//   - 目录不存在是空操作，不可读也不是错误，
//     使重复调用幂等，且在全新安装上安全；
//   - 单个删除失败会被跳过，绝不中止清理；
//   - maxAge <= 0 完全禁用清理，而不是删除所有内容，
//     因此配置错误的调用方也无法清空目录。
//
// 返回的计数仅用于可观测性/测试。生产调用方必须
// 忽略该错误：清理是尽力而为的，绝不能让工具运行失败。
func CleanSpillArtifacts(dir string, maxAge time.Duration) (removed int, err error) {
	if maxAge <= 0 || strings.TrimSpace(dir) == "" {
		return 0, nil
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return 0, nil
		}
		return 0, readErr
	}
	cutoff := time.Now().Add(-maxAge)
	for _, entry := range entries {
		if !spillArtifactName(entry.Name()) {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil || !info.Mode().IsRegular() || !info.ModTime().Before(cutoff) {
			continue
		}
		if os.Remove(filepath.Join(dir, entry.Name())) == nil {
			removed++
		}
	}
	return removed, nil
}
