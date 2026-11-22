package tools

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// 本文件给溢出产出清理加磁盘容量闸门：除了按时间清理（CleanSpillArtifacts），
// 还可以按总字节上限淘汰最旧产出，防止批量任务短时间打满磁盘。

// spillFile 是一条符合产出契约的文件的大小与修改时间，用于按 mtime 淘汰。
type spillFile struct {
	path    string
	size    int64
	modTime int64 // UnixNano，便于排序
}

// TrimSpillArtifacts 把 dir 中符合产出契约的文件按 mtime 升序淘汰，
// 直到总字节数 <= maxBytes。返回 (删除数, 释放字节数, 错误)。
//
// 保守性：只碰文件名完全符合契约的普通文件；目录不存在/不可读时按空处理；
// 单个删除失败跳过；maxBytes <= 0 完全禁用（不做任何删除）。
// 调用方必须忽略错误：清理是尽力而为，绝不让工具运行失败。
func TrimSpillArtifacts(dir string, maxBytes int64) (removed int, freed int64, err error) {
	if maxBytes <= 0 || strings.TrimSpace(dir) == "" {
		return 0, 0, nil
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return 0, 0, nil
		}
		return 0, 0, readErr
	}
	files := make([]spillFile, 0, len(entries))
	var total int64
	for _, entry := range entries {
		if !spillArtifactName(entry.Name()) {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil || !info.Mode().IsRegular() {
			continue
		}
		size := info.Size()
		if size < 0 {
			size = 0
		}
		files = append(files, spillFile{
			path: filepath.Join(dir, entry.Name()),
			size: size, modTime: info.ModTime().UnixNano(),
		})
		total += size
	}
	if total <= maxBytes {
		return 0, 0, nil
	}
	// 最旧优先；相同时间按路径保证确定性。
	sort.Slice(files, func(i, j int) bool {
		if files[i].modTime == files[j].modTime {
			return files[i].path < files[j].path
		}
		return files[i].modTime < files[j].modTime
	})
	for _, f := range files {
		if total <= maxBytes {
			break
		}
		if os.Remove(f.path) == nil {
			removed++
			freed += f.size
			total -= f.size
		}
	}
	return removed, freed, nil
}
