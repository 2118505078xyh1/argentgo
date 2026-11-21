package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// 本文件提供一个纯 stdlib 的「文件新鲜度」原语，仿 cline 的 FileContextTracker：
// 记录 agent 上次读/写某文件的时刻与当时的 size/modtime 快照；当磁盘文件在
// 快照之后被外部改动（size 或 modtime 变化，甚至文件消失）时，认为该文件已
// stale，并给出「改前重读」的可读提示。
//
// 它刻意不接入任何工具：没有生产调用方，除内存登记外没有副作用。接线层如需
// 使用，应在读取成功后调用 MarkRead、在写入成功后调用 MarkWrite，并在准备编辑
// 前用 IsStale/StaleWarning 判断是否需要先重读。

// fileFreshnessNow 是变量，便于测试固定时钟。
var fileFreshnessNow = time.Now

// fileFreshnessEntry 是单个被跟踪文件的记录：路径、上次读/写时刻与
// size/modtime 快照。
type fileFreshnessEntry struct {
	path      string    // 首次登记时调用方传入的路径拼写，仅用于展示
	lastRead  time.Time // 上次读取时刻
	lastWrite time.Time // 上次写入时刻（零值表示尚未由本 agent 写过）
	size      int64     // 上次快照的文件大小
	modTime   time.Time // 上次快照的文件修改时间
	tracked   bool      // 是否已有有效的 size/modtime 快照
}

// FileFreshness 以规范化路径为键，跟踪多个文件的新鲜度。
// 零值可直接使用；并发安全。
type FileFreshness struct {
	mu      sync.RWMutex
	entries map[string]*fileFreshnessEntry
}

// NewFileFreshness 构造一个空的跟踪器。零值 FileFreshness 同样可用。
func NewFileFreshness() *FileFreshness {
	return &FileFreshness{entries: make(map[string]*fileFreshnessEntry)}
}

// fileFreshnessKey 规范化路径：先 Abs 再 Clean，Windows 上折叠大小写。
// 这样 "./a.txt"、"dir/../a.txt" 与绝对路径会落到同一个键。
// 它只返回新字符串，绝不修改入参。
func fileFreshnessKey(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = filepath.Clean(path)
	} else {
		abs = filepath.Clean(abs)
	}
	if runtime.GOOS == "windows" {
		return strings.ToLower(abs)
	}
	return abs
}

// fileFreshnessStat 读取当前 size/modtime。文件不可读时 ok=false，
// 调用方据此把「文件消失」也视作外部改动。
func fileFreshnessStat(path string) (size int64, modTime time.Time, ok bool) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, time.Time{}, false
	}
	return info.Size(), info.ModTime(), true
}

// entryFor 在已持有写锁的前提下取出（或创建）key 对应的记录。
// displayPath 保留首次登记时调用方传入的拼写，仅用于提示展示，不参与比较。
func (f *FileFreshness) entryFor(key, displayPath string) *fileFreshnessEntry {
	if f.entries == nil {
		f.entries = make(map[string]*fileFreshnessEntry)
	}
	e := f.entries[key]
	if e == nil {
		e = &fileFreshnessEntry{path: displayPath}
		f.entries[key] = e
	}
	return e
}

// MarkRead 记录一次成功读取：更新时间戳并把当前磁盘状态作为新快照。
// 这样后续只有「这次读取之后」发生的外部改动才会被判为 stale。
// 若文件此刻不可读（例如刚被删除），保留旧快照，不制造虚假的「新鲜」状态。
func (f *FileFreshness) MarkRead(path string) {
	key := fileFreshnessKey(path)
	f.mu.Lock()
	defer f.mu.Unlock()
	e := f.entryFor(key, path)
	e.lastRead = fileFreshnessNow()
	// 持锁期间取样，保证并发 Mark* 中最后拿到锁的那个留下最新快照。
	if size, modTime, ok := fileFreshnessStat(key); ok {
		e.size = size
		e.modTime = modTime
		e.tracked = true
	}
}

// MarkWrite 记录一次成功写入：更新时间戳并把写入后的磁盘状态作为新快照，
// 从而把「我们自己造成的改动」与「外部改动」区分开——写完后不应再报 stale。
// 调用方必须在写系统调用完成之后调用本方法，否则快照会取到写入前的状态。
func (f *FileFreshness) MarkWrite(path string) {
	key := fileFreshnessKey(path)
	f.mu.Lock()
	defer f.mu.Unlock()
	e := f.entryFor(key, path)
	e.lastWrite = fileFreshnessNow()
	if size, modTime, ok := fileFreshnessStat(key); ok {
		e.size = size
		e.modTime = modTime
		e.tracked = true
	}
}

// IsStale 判断 path 自上次读/写快照之后是否被外部改动。
// 未跟踪（从未 Mark*）的文件返回 false：没有基线就无从判断变化。
// 文件消失或不可读同样视为 stale。
func (f *FileFreshness) IsStale(path string) bool {
	key := fileFreshnessKey(path)

	f.mu.RLock()
	defer f.mu.RUnlock()
	e := f.entries[key]
	if e == nil || !e.tracked {
		return false
	}
	curSize, curModTime, ok := fileFreshnessStat(key)
	if !ok {
		return true
	}
	return curSize != e.size || !curModTime.Equal(e.modTime)
}

// StaleWarning 在文件 stale 时返回可读提示，否则返回空串。
// 提示中的固定短语保持连续，便于 UI 匹配与测试断言；路径附在括号内，
// 使用调用方传入的拼写（而非小写化的规范化键），读起来更自然。
func (f *FileFreshness) StaleWarning(path string) string {
	if !f.IsStale(path) {
		return ""
	}
	return fmt.Sprintf("文件已被外部修改，建议改前重读（%s）", path)
}
