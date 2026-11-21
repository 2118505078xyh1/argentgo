package memory

// 溢出目录统计原语。AnalyzeSpillDir 以只读方式扫描 spill 目录，汇总其中
// 规范命名工件的数量、总字节与最旧/最新修改时间。它刻意不挂在 Bank 上，
// 也没有任何调用方：只提供独立的统计能力，不改变现有落盘/恢复/清理行为。

import (
	"fmt"
	"os"
	"path/filepath"
)

// SpillStats 是对 spill 目录一次扫描的汇总结果。
// Count 为符合命名约定的工件数量；TotalBytes 为这些文件的字节总和；
// OldestTS 与 NewestTS 分别为其中最旧、最新的修改时间（Unix 秒，来自
// os.Stat 的 ModTime）。没有任何工件时四个字段均为零值。
type SpillStats struct {
	Count      int
	TotalBytes int64
	OldestTS   int64
	NewestTS   int64
}

// AnalyzeSpillDir 扫描 dir（即 spill 目录本身），只统计符合本包落盘命名
// 约定（isSpillFileName）的工件文件：累计数量与字节数，并记录其中最旧与
// 最新的修改时间。
//
// 行为约定：
//   - 目录不存在时返回零值与 nil，不视为错误；
//   - 子目录、命名不规范的文件一律忽略，不参与统计；
//   - 全程只读，不创建、修改或删除任何文件；
//   - 符合命名约定的文件若 os.Stat 失败，则包装错误返回。
func AnalyzeSpillDir(dir string) (SpillStats, error) {
	var stats SpillStats
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return SpillStats{}, nil
		}
		return SpillStats{}, fmt.Errorf("memory: analyze spill dir: %w", err)
	}
	for _, e := range entries {
		// 子目录与命名不规范的文件直接跳过，连 stat 都不做。
		if e.IsDir() || !isSpillFileName(e.Name()) {
			continue
		}
		info, err := os.Stat(filepath.Join(dir, e.Name()))
		if err != nil {
			return SpillStats{}, fmt.Errorf("memory: analyze spill stat %s: %w", e.Name(), err)
		}
		ts := info.ModTime().Unix()
		stats.Count++
		stats.TotalBytes += info.Size()
		if stats.Count == 1 {
			stats.OldestTS, stats.NewestTS = ts, ts
			continue
		}
		if ts < stats.OldestTS {
			stats.OldestTS = ts
		}
		if ts > stats.NewestTS {
			stats.NewestTS = ts
		}
	}
	return stats, nil
}
