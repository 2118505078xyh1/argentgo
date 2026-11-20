package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// writeFileNoFollow 把 data 写入 path，且不跟随目标路径处的符号链接。它是
// 一个仅依赖标准库、可直接替换 os.WriteFile 的实现，供 CLI 的导出路径
// （sessions export -o、bug --out）使用。
//
// 保护是分层进行的：
//   - 打开之前先拒绝 path 处已存在的符号链接；
//   - 在 Unix 上，打开本身使用 O_NOFOLLOW，因此在检查与创建之间被替换的
//     链接会被内核原子地拒绝；
//   - 关闭后再次检查 path，从而检测到替换，而不是悄悄接受。
//
// 在没有可移植 O_NOFOLLOW 的平台上（Windows 等），Lstat 守卫是唯一的保护；
// 它们仍能阻止所报告的攻击：用户把导出指向一个已存在的符号链接（如
// ~/.bashrc）。Windows 的 junction 和其他重解析点表现为 os.ModeIrregular，
// 同样会被拒绝。
func writeFileNoFollow(path string, data []byte, perm os.FileMode) error {
	if fi, err := os.Lstat(path); err == nil {
		if isNoFollowViolation(fi.Mode()) {
			return fmt.Errorf("refusing to write through symlink or reparse point %q", path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	f, err := openNoFollowFile(path, perm)
	if err != nil {
		return err
	}
	n, writeErr := f.Write(data)
	if writeErr == nil && n < len(data) {
		writeErr = io.ErrShortWrite
	}
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}

	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if isNoFollowViolation(fi.Mode()) {
		return fmt.Errorf("refusing to write through symlink or reparse point %q", path)
	}
	return nil
}

// isNoFollowViolation 报告某个路径的 mode 是否把它标记为绝不能写入穿过的
// 链接。ModeIrregular 涵盖 Windows junction 和其他重解析点；Unix 的 Lstat
// 对普通文件从不设置它。
func isNoFollowViolation(mode os.FileMode) bool {
	return mode&(os.ModeSymlink|os.ModeIrregular) != 0
}
