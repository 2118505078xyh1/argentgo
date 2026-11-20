//go:build !windows

package security

import (
	"os"
	"path/filepath"
)

// realPathExisting 在 POSIX 系统上解析符号链接。filepath.EvalSymlinks 会跟随
// 整条链；POSIX 没有需要处理的目录 junction。
func realPathExisting(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}

// isLinkInfo 报告 info 是否描述一个符号链接。POSIX 没有 junction，
// 因此模式位是唯一需要的链接标记。
func isLinkInfo(info os.FileInfo) bool {
	return info != nil && info.Mode()&os.ModeSymlink != 0
}

// normalizeLinkTarget 在 POSIX 上是空操作：Readlink 原样返回目标。
func normalizeLinkTarget(path string) string { return path }
