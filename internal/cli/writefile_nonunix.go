//go:build !unix

package cli

import "os"

// 在 O_NOFOLLOW 不可移植的平台上（Windows 及其他非 unix 目标），
// openNoFollowFile 回退为普通打开。那里的符号链接防御由
// writeFileNoFollow 的 Lstat 守卫承担；Go 把 Windows 符号链接报告为
// os.ModeSymlink。
func openNoFollowFile(path string, perm os.FileMode) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
}
