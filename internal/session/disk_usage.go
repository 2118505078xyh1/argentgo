package session

import (
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DiskUsage 是一次磁盘占用统计的快照，只描述普通文件，不含目录本身与符号链接。
type DiskUsage struct {
	Files  int              // 计入的普通文件数量（极端情况下钳制到 math.MaxInt）。
	Bytes  int64            // 所有计入文件的字节总和（溢出时钳制到 math.MaxInt64）。
	ByKind map[string]int64 // 按 kind 聚合的字节数；键恒为 session/archive/other。
}

// kind 分类的固定键，保证任何目录下输出的键集合一致（确定性）。
const (
	diskKindSession = "session"
	diskKindArchive = "archive"
	diskKindOther   = "other"
)

// MeasureDiskUsage 递归统计 dir 下所有普通文件的数量与字节占用，并按扩展名聚合到 ByKind。
//
// 分类规则（大小写不敏感）：
//   - .jsonl 结尾 → session
//   - .zip 或 .tar.gz 结尾 → archive
//   - 其余 → other
//
// 安全性与确定性：
//   - 全程使用 Lstat 语义，不跟随任何符号链接；符号链接本身既不计入也不展开，避免逃出 dir 或形成环。
//   - 收集到的普通文件按路径全局排序后再累加，相同目录结构恒定得到相同结果，溢出钳制与遍历次序无关。
//   - 单文件大小与累计字节在接近 int64/int 上限时钳制到最大值，不会发生环绕变负。
//   - 纯读操作：不创建、修改或删除任何文件。
//
// 当 dir 不存在、不是目录、本身是符号链接，或遍历中遇到无法访问的文件时返回错误。
func MeasureDiskUsage(dir string) (DiskUsage, error) {
	// 三个键恒定存在且初值为 0，使输出形状与目录内容无关。
	usage := DiskUsage{
		ByKind: map[string]int64{
			diskKindSession: 0,
			diskKindArchive: 0,
			diskKindOther:   0,
		},
	}

	rootInfo, err := os.Lstat(dir)
	if err != nil {
		return usage, fmt.Errorf("session: measure disk usage: stat %s: %w", dir, err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 {
		return usage, fmt.Errorf("session: measure disk usage: root is symlink: %s", dir)
	}
	if !rootInfo.IsDir() {
		return usage, fmt.Errorf("session: measure disk usage: not a directory: %s", dir)
	}

	type regularFile struct {
		path string
		size int64
	}
	var files []regularFile
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		// 符号链接：既不计入，也不跟随（WalkDir 默认不展开为目录）。
		if diskUsageIsSymlink(d) {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		// 跳过管道、设备、套接字等特殊文件，只统计普通文件。
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		size := info.Size()
		if size < 0 { // 防御性处理：负大小视为 0，避免污染累计值。
			size = 0
		}
		files = append(files, regularFile{path: path, size: size})
		return nil
	})
	if err != nil {
		return usage, fmt.Errorf("session: measure disk usage: %w", err)
	}

	// 全局按路径排序后累加，保证结果与目录遍历次序无关。
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	for _, f := range files {
		kind := diskUsageKind(f.path)
		usage.ByKind[kind] = clampAddInt64(usage.ByKind[kind], f.size)
		usage.Bytes = clampAddInt64(usage.Bytes, f.size)
		if usage.Files < math.MaxInt {
			usage.Files++
		}
	}

	return usage, nil
}

// diskUsageIsSymlink 判断遍历到的条目是否为符号链接。独立成函数是为了在无创建
// 符号链接权限的平台上也能直接验证该判定分支。
func diskUsageIsSymlink(d fs.DirEntry) bool {
	return d.Type()&os.ModeSymlink != 0
}

// diskUsageKind 按文件名后缀判定聚合类别；大小写不敏感。
func diskUsageKind(path string) string {
	name := strings.ToLower(filepath.Base(path))
	switch {
	case strings.HasSuffix(name, ".jsonl"):
		return diskKindSession
	case strings.HasSuffix(name, ".tar.gz"), strings.HasSuffix(name, ".zip"):
		return diskKindArchive
	default:
		return diskKindOther
	}
}

// clampAddInt64 返回 a+b，在逼近 int64 上限时钳制到 math.MaxInt64，避免溢出环绕为负。
func clampAddInt64(a, b int64) int64 {
	if b > 0 && a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}
