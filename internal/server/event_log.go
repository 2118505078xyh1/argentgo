package server

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"example.com/argentgo/internal/agent"
)

// eventLogFileMaxEntries 限制事件日志保留的条目数。到上限后按行淘汰最旧的，
// 使重连续放的磁盘量有界；内存 hub 的 256 条回放窗口仍为主要路径。
const eventLogFileMaxEntries = 4096

// EventLog 把 hub 发布的事件以 JSONL 追加到磁盘，供 SSE 客户端在
// 超出内存回放窗口后用 Last-Event-ID 续放。每条记录形如
// {"seq":N,"event":{...agent.Event...}}。它刻意只在 opt-in 挂载时使用；
// 未挂载时 hub 的发布与回放逐字节不变。
type EventLog struct {
	mu      sync.Mutex
	path    string
	entries int
}

// NewEventLog 构造写入 path 的事件日志。它不创建文件：首次 Append 时才落盘。
func NewEventLog(path string) *EventLog {
	return &EventLog{path: path}
}

type eventLogEntry struct {
	Seq   uint64      `json:"seq"`
	Event agent.Event `json:"event"`
}

// append 把一条事件追加到日志。达到条目上限后重写文件，仅保留最近
// eventLogFileMaxEntries 条，保证磁盘与重连回放都有界。
func (l *EventLog) append(seq uint64, ev agent.Event) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	line, err := json.Marshal(eventLogEntry{Seq: seq, Event: ev})
	if err != nil {
		f.Close()
		return err
	}
	_, err = f.Write(append(line, '\n'))
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	l.entries++
	if l.entries > eventLogFileMaxEntries {
		return l.trimLocked()
	}
	return nil
}

// trimLocked 在已持有 l.mu 时截断日志，仅保留最近 eventLogFileMaxEntries 条。
func (l *EventLog) trimLocked() error {
	f, err := os.Open(l.path)
	if err != nil {
		return err
	}
	var lines [][]byte
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		lines = append(lines, line)
	}
	scanErr := scanner.Err()
	f.Close()
	if scanErr != nil {
		return scanErr
	}
	if len(lines) <= eventLogFileMaxEntries {
		l.entries = len(lines)
		return nil
	}
	lines = lines[len(lines)-eventLogFileMaxEntries:]
	tmp := l.path + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	for _, line := range lines {
		if _, err := out.Write(append(line, '\n')); err != nil {
			out.Close()
			return err
		}
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, l.path); err != nil {
		return err
	}
	l.entries = len(lines)
	return nil
}

// after 返回 seq 严格大于给定游标的事件，按发布顺序。读不到文件或解析失败
// 的个别行会被跳过，使损坏的日志不阻塞重连（最坏只是少放几条历史）。
func (l *EventLog) after(seq uint64) []eventLogEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.Open(l.path)
	if err != nil {
		return nil
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var out []eventLogEntry
	for scanner.Scan() {
		var entry eventLogEntry
		if json.Unmarshal(scanner.Bytes(), &entry) != nil || entry.Seq <= seq {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// initEntries 在挂载时统计已有条目数，使首次 Append 的淘汰判断基于真实大小。
func (l *EventLog) initEntries() {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.Open(l.path)
	if err != nil {
		return
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		l.entries++
	}
}
