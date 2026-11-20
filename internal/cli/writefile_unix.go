//go:build unix

package cli

import (
	"os"
	"syscall"
)

// openNoFollowFile 使用 O_NOFOLLOW 以写入方式打开 path，因此绝不会跟随
// 最后一段路径组件上的符号链接。只有权限位被传给系统调用；umask 仍然生效，
// 与 os.Create/os.WriteFile 的语义一致。
func openNoFollowFile(path string, perm os.FileMode) (*os.File, error) {
	fd, err := syscall.Open(path,
		syscall.O_WRONLY|syscall.O_CREAT|syscall.O_TRUNC|syscall.O_NOFOLLOW,
		uint32(perm.Perm()))
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}
