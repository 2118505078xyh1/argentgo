package jobs

import (
	"fmt"
	"io"
	"os"
	"unicode/utf8"
)

// sliceOutputByOffset 从 data 的绝对字节 offset 处切出一段可安全显示的 UTF-8 输出。
//
// 返回值含义：
//   - chunk：从第一个完整 rune 开始、至多 limit 字节的子切片；起止边界绝不在 UTF-8 字符中间。
//   - nextOffset：调用方下次读取应使用的偏移；它越过本次消费的全部字节（含为对齐而跳过的
//     前置残片），因此按此偏移连续读取互不重叠、可幂等拼接。
//   - truncated：本次结果是否为有损/被截断的视图。offset 落在 rune 中间而跳过前置残片
//     （对应审计里的"有损耗标记"），或 limit 截断了剩余数据，都会置为 true。
//
// 约定：offset 为负时钳到 0；offset 不小于 len(data) 时返回空块；limit <= 0 表示不限制
// 字节数，读取全部剩余数据。CJK 等多字节字符按 rune 对齐，绝不会被切断。
func sliceOutputByOffset(data []byte, offset int64, limit int) (chunk []byte, nextOffset int64, truncated bool) {
	if offset < 0 {
		offset = 0
	}
	n := int64(len(data))
	if offset >= n {
		return nil, n, false
	}

	start := int(offset)
	// 起始对齐：跳过落在 UTF-8 序列中间的残片字节（有损耗）。
	for start < len(data) && !utf8.RuneStart(data[start]) {
		start++
	}
	if start >= len(data) {
		// 剩余全是残片，全部跳过；标记为有损。
		return nil, n, true
	}

	truncated = start > int(offset)
	end := len(data)
	if limit > 0 && limit < end-start {
		truncated = true
		end = start + limit
		// 结束对齐：回退到 rune 边界，绝不在 UTF-8 中间切。
		for end > start && !utf8.RuneStart(data[end]) {
			end--
		}
		// 保证前进：单个 rune 比 limit 还宽时整体纳入，
		// 宁可略微超出 limit，也不返回空块让调用方原地打转。
		if end == start {
			_, size := utf8.DecodeRune(data[start:])
			end = start + size
		}
	}

	return data[start:end], int64(end), truncated
}

// TailFromOffset 按绝对字节偏移增量读取作业日志，是 Tail 的可选补充（默认不启用）。
// 现有 Tail 的行为完全不变；只有显式调用本方法才能获得按偏移读取、可幂等续读的能力。
//
// 返回的 nextOffset 是文件中的绝对字节偏移，可直接作为下次调用的 offset；
// truncated 为 true 表示本次输出有损或被 limit 截断，调用方应据此决定是否继续读取。
// 这在日志持续追加时尤其有用：调用方只需保存 last offset，重连后从该偏移继续，
// 既不重复已消费的内容，也不会因按行 Tail 丢失不完整的末行。
func (r *Registry) TailFromOffset(id string, offset int64, limit int) (chunk string, nextOffset int64, truncated bool, err error) {
	job, err := r.Get(id)
	if err != nil {
		return "", 0, false, err
	}
	file, err := os.Open(job.LogPath)
	if err != nil {
		return "", 0, false, fmt.Errorf("jobs: read %s: %w", job.LogPath, err)
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return "", 0, false, fmt.Errorf("jobs: stat %s: %w", job.LogPath, err)
	}
	size := info.Size()
	if offset < 0 {
		offset = 0
	}
	if offset >= size {
		return "", size, false, nil
	}

	n := size - offset
	if limit > 0 && int64(limit) < n {
		// 多读至多一个 rune 的宽度，确保首尾的 rune 都能完整对齐。
		n = int64(limit) + utf8.UTFMax
		if n > size-offset {
			n = size - offset
		}
	}
	data := make([]byte, n)
	read, err := file.ReadAt(data, offset)
	if err != nil && err != io.EOF {
		return "", offset, false, fmt.Errorf("jobs: read %s: %w", job.LogPath, err)
	}
	data = data[:read]

	chunkBytes, consumed, truncated := sliceOutputByOffset(data, 0, limit)
	return string(chunkBytes), offset + consumed, truncated, nil
}
