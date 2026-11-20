package update

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	// StaleSuffix 在替换过程中附加到旧二进制文件上。
	StaleSuffix = ".old"

	applyAttempts = 10
	applyDelay    = 100 * time.Millisecond
)

// Apply 用新下载的 dest 替换 target。
//
// 在 Windows 上正在运行的可执行文件无法被覆盖，但可以重命名，因此
// target 先移到 "<name>.old"，下载文件占据其位置，而更早更新遗留的
// ".old" 会被先删除。在其他平台上单次重命名即为
// 原子操作。重命名会短暂重试，因为杀毒扫描程序和索引器
// 会短暂锁定新文件。
func Apply(dest, target string) error {
	if strings.TrimSpace(dest) == "" || strings.TrimSpace(target) == "" {
		return errors.New("update: both the staged file and the target are required")
	}
	info, err := os.Stat(dest)
	if err != nil {
		return fmt.Errorf("update: staged binary %s: %w", dest, err)
	}
	if info.IsDir() {
		return fmt.Errorf("update: staged path %s is a directory", dest)
	}
	if looksLikeZip(dest) {
		return fmt.Errorf("update: staged file %s is a zip archive, not an executable", dest)
	}
	if looksLikeGzip(dest) {
		return fmt.Errorf("update: staged file %s is a gzip archive, not an executable", dest)
	}
	directory := filepath.Dir(target)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("update: create %s: %w", directory, err)
	}
	if runtime.GOOS == "windows" {
		return applyWindows(dest, target)
	}
	return applyUnix(dest, target)
}

func applyWindows(dest, target string) error {
	stale := target + StaleSuffix
	// 上一次更新可能留下了它的 .old（正在运行的二进制文件无法
	// 被删除），所以在重命名前先腾出空间。
	if err := os.Remove(stale); err != nil && !errors.Is(err, os.ErrNotExist) {
		// 非致命：下面的重命名会暴露真正的问题。
		_ = err
	}
	hadTarget := false
	if _, err := os.Stat(target); err == nil {
		hadTarget = true
		if err := retryRename(target, stale); err != nil {
			return fmt.Errorf("update: move the current binary aside: %w", err)
		}
	}
	if err := retryRename(dest, target); err != nil {
		if hadTarget {
			_ = retryRename(stale, target)
		}
		return fmt.Errorf("update: install the new binary at %s: %w", target, err)
	}
	return nil
}

func applyUnix(dest, target string) error {
	_ = os.Remove(target + StaleSuffix)
	if err := os.Chmod(dest, 0o755); err != nil {
		return fmt.Errorf("update: mark %s executable: %w", dest, err)
	}
	if err := retryRename(dest, target); err == nil {
		return nil
	}
	// 下载目录可能位于另一个文件系统上；先复制到 target 旁边，
	// 再从那里重命名，这样仍是原子操作。
	temp, err := os.CreateTemp(filepath.Dir(target), ".argent-install-*")
	if err != nil {
		return fmt.Errorf("update: stage beside %s: %w", target, err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	source, err := os.Open(dest)
	if err != nil {
		temp.Close()
		return fmt.Errorf("update: open %s: %w", dest, err)
	}
	_, copyErr := io.Copy(temp, source)
	source.Close()
	if copyErr != nil {
		temp.Close()
		return fmt.Errorf("update: copy %s: %w", dest, copyErr)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("update: flush %s: %w", tempName, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("update: close %s: %w", tempName, err)
	}
	if err := os.Chmod(tempName, 0o755); err != nil {
		return fmt.Errorf("update: mark %s executable: %w", tempName, err)
	}
	if err := retryRename(tempName, target); err != nil {
		return fmt.Errorf("update: install the new binary at %s: %w", target, err)
	}
	_ = os.Remove(dest)
	return nil
}

// looksLikeZip 报告文件是否以 PK zip 签名开头。这是
// 最后的防线：即使将来某条下载路径忘记解包
// 压缩包，Apply 也绝不能把 zip 安装为运行中的可执行文件。
func looksLikeZip(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = file.Close() }()
	var magic [4]byte
	if _, err := io.ReadFull(file, magic[:]); err != nil {
		return false
	}
	return magic[0] == 'P' && magic[1] == 'K' &&
		((magic[2] == 3 && magic[3] == 4) || // local header
			(magic[2] == 5 && magic[3] == 6) || // empty archive
			(magic[2] == 7 && magic[3] == 8)) // spanned archive
}

// looksLikeGzip 报告文件是否以 gzip 魔数开头。它
// 捕获未经解包就到达 Apply 的原始 .tar.gz/.tgz，
// looksLikeZip 会漏掉这种情况，而它本会被当作二进制安装。
func looksLikeGzip(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = file.Close() }()
	var magic [2]byte
	if _, err := io.ReadFull(file, magic[:]); err != nil {
		return false
	}
	return magic[0] == 0x1f && magic[1] == 0x8b
}

func retryRename(from, to string) error {
	var err error
	for attempt := 0; attempt < applyAttempts; attempt++ {
		if err = os.Rename(from, to); err == nil {
			return nil
		}
		if attempt < applyAttempts-1 {
			time.Sleep(applyDelay)
		}
	}
	return err
}
