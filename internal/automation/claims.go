package automation

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// errBusy 报告某 id 已有进行中的运行。它是唯一的
// errBusy 报告某 id 已有进行中的运行。它是 daemon 循环与 RunNow 共享的唯一
// 认领信号，使两条路径即使在各自独立的进程中也遵守同一条"每个 id 至多一次运行"规则。
var errBusy = errors.New("automation: task is already running")

// staleClaimAfter 是新进程把认领视为被放弃之前该认领必须达到的年龄。正常运行在完成时释放认领；这只
// 视为被放弃。正常运行在完成时释放认领；这只
// 用于从运行中途死掉的进程中恢复。它刻意设得很长，使
// 缓慢但仍存活的任务永不被重复。
const staleClaimAfter = time.Hour

// 用于从运行中途死掉的进程中恢复。它刻意设得很长，使缓慢但仍存活的任务永不被重复。
// claimDir 保存跨进程的进行中标记和每个 id 的运行计数器。它位于 store 目录中，RunNow 的每个调用方和
// 每个 daemon 都共享该目录，即使它们的日志目录不同。
func (s *Store) claimDir() string { return filepath.Join(s.dir, ".claims") }

// claimFileName 把任意（可能是手写的）任务 id 映射为安全、稳定的文件名，而不把它当作路径组成部分信任。
// 稳定的文件名，而不把它当作路径组成部分信任。
func claimFileName(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}

// claim 原子地为一次运行预留 id 并返回该运行的单调
// 序列号。预留是用 O_EXCL 创建的文件，因此
// claim 原子地为一次运行预留 id 并返回该运行的单调序列号。预留是用 O_EXCL 创建的文件，因此即使 daemon 和 RunNow 运行在不同进程中，它也对它们可见。
// 得到 nil 错误的调用方必须在其运行结束时调用 release。
func (s *Store) claim(id string) (uint64, error) {
	dir := s.claimDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	key := claimFileName(id)
	lock := filepath.Join(dir, key+".lock")
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if errors.Is(err, fs.ErrExist) && claimIsStale(lock) {
		// 崩溃的进程无法释放其认领；在宽限期后回收它，
		// 崩溃的进程无法释放其认领；在宽限期后回收它，使重启的 daemon 不会永远卡住。
		_ = os.Remove(lock)
		f, err = os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	}
	if err != nil {
		return 0, errBusy
	}
	_, _ = fmt.Fprintf(f, "%d\n", time.Now().UnixNano())
	_ = f.Close()
	seq, err := s.nextSeq(dir, key)
	if err != nil {
		_ = os.Remove(lock)
		return 0, err
	}
	return seq, nil
}

// release 丢弃 id 的进行中预留。释放从未被认领的 id 是无害的。
// 从未被认领的 id 是无害的。
func (s *Store) release(id string) {
	_ = os.Remove(filepath.Join(s.claimDir(), claimFileName(id)+".lock"))
}

// nextSeq 返回下一个每 id 运行序列号。它只在持有对应认领时调用，因此读-改-写跨进程是安全的。计数器在释放和重启后保留，这使它成为
// 对应认领被持有，因此读-改-写跨
// 进程是安全的。计数器在释放和重启后保留，这使它成为
// 稳定、无冲突的日志文件名基础。
func (s *Store) nextSeq(dir, key string) (uint64, error) {
	path := filepath.Join(dir, key+".seq")
	var n uint64
	if data, err := os.ReadFile(path); err == nil {
		n, _ = strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return 0, err
	}
	n++
	if err := writeAtomic(path, []byte(strconv.FormatUint(n, 10)+"\n")); err != nil {
		return 0, err
	}
	return n, nil
}

// claimIsStale 报告认领文件是否旧到可以被放弃。不可读、不可用或旧的标记都被视为陈旧，使它不能永久卡住任务。
// 不可读、不可用或旧的标记被视为陈旧，使它不能
// 永久卡住任务。
//
// 新鲜度必须来自文件的修改时间，而非其中写入的标记：
// 新鲜度必须来自文件的修改时间，而非其中写入的标记：os.OpenFile(O_EXCL) 在所有者写入标记之前先创建一个空的锁文件，而在那个窗口里基于标记的
// 所有者写入标记，在那个窗口里基于标记的检查读到空
// 检查会读到空标记，把它视为畸形，并让第二个调用方窃取一个进行中的
// 运行。标记留在文件中仅供人工检查；mtime 由同一个创建/写入步骤记录，在文件出现时已经是最新的。
// 由同一个创建/写入步骤记录，在文件出现时已是最新。
func claimIsStale(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		// 标记消失或无法检查：不要卡住任务。
		return true
	}
	return !info.Mode().IsRegular() || time.Since(info.ModTime()) > staleClaimAfter
}
