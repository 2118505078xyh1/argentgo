package tools

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ErrSpillCapacity 表示工件目录已达容量上限，不应再写入新工件。
// 调用方据此退化为不带恢复链接的截断。
var ErrSpillCapacity = errors.New("tools: output spill capacity reached")

// spillMaxBytesEnv 是工件目录总容量上限的 env（字节）。
const spillMaxBytesEnv = "ARGENT_OUTPUT_SPILL_MAX_BYTES"

func spillMaxBytes() int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(os.Getenv(spillMaxBytesEnv)), 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// directoryBytes 累加目录下所有普通文件的字节数；符号链接与子目录递归统计。
func directoryBytes(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // 不可读的项跳过，容量检查是尽力而为
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total, err
}
