package tools

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

// todoPublishMu 串行化最终的“重命名到位”步骤。Windows 上多个写入者同时
// 替换同一路径时，os.Rename 会因目标被占用而返回 ERROR_ACCESS_DENIED；
// 而“先移除目标再重命名”的降级路径本身也有竞态窗口。把整个发布临界区
// 放在进程内锁下即可消除同进程内的竞争，跨进程的极端情况仍由降级路径兜底。
var todoPublishMu sync.Mutex

// atomicWriteTodo 把 data 原子地写入 path：先在同一目录暂存临时文件，
// 写入并 Sync 后再重命名到位。读取者因此只会看到完整的旧文件或完整的新文件，
// 崩溃也不会把目标（例如 todos.json）截成半截 JSON。
// perm 用于设置最终文件的权限位；任何失败都会清理临时文件。
func atomicWriteTodo(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = tmp.Close()
			_ = os.Remove(name)
		}
	}()

	// CreateTemp 创建的临时文件固定为 0o600，这里改回调用方期望的权限，
	// 使覆盖写与首次写入得到的权限位一致。
	if err := tmp.Chmod(perm); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	todoPublishMu.Lock()
	err = renameTodoTemp(name, path)
	todoPublishMu.Unlock()
	if err != nil {
		return err
	}
	committed = true
	return nil
}

// renameTodoTemp 把已同步的临时文件重命名为目标文件。
// 现代 Go 在 Windows 上的 os.Rename 已带 MOVEFILE_REPLACE_EXISTING，
// 能直接覆盖已有文件；但个别文件系统仍拒绝替换。仅当原子替换失败且确认
// 目标存在时，才降级为“先移除目标再重命名”。该降级路径存在竞态窗口
// （目标消失到重命名之间），因此只在必要时走，且不吞掉任何一步的错误。
func renameTodoTemp(tmp, path string) error {
	err := os.Rename(tmp, path)
	if err == nil {
		return nil
	}
	if runtime.GOOS != "windows" {
		return err
	}
	// 目标不存在时的失败原样上报，不做无意义的移除。
	if _, statErr := os.Stat(path); statErr != nil {
		return err
	}
	// 移除失败时保留最初的 rename 错误，避免误报成移除成功。
	if removeErr := os.Remove(path); removeErr != nil {
		return err
	}
	if retryErr := os.Rename(tmp, path); retryErr != nil {
		return retryErr
	}
	return nil
}
