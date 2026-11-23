// Package timeline 提供 TUI 终端输出的时间线录制与回放（学自 opencode
// packages/simulation/src/recording.ts）。
//
// 录制格式是三类 JSONL 事件：header（版本/列行/编码）、output（at_ms + base64
// 数据）、resize（列行变化）。回放器按时间戳驱动输出，支持固定速度或即时回放，
// 用于端到端测试、bug 复现与教学演示。纯标准库、无网络、无 WebSocket。
//
// 本包不侵入 TUI 主循环：调用方用一个 Recorder 包装输出 Writer，把每次绘制
// 的字节写进去即可；回放时用 Player 把字节按时间线吐给任意 Writer。
package timeline

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

const formatVersion = 1

// EventKind 是事件种类。
type EventKind string

const (
	KindHeader EventKind = "header"
	KindOutput EventKind = "output"
	KindResize EventKind = "resize"
)

// Header 是录制头（第一行，必含）。
type Header struct {
	Version  int    `json:"version"`
	Cols     int    `json:"cols"`
	Rows     int    `json:"rows"`
	Encoding string `json:"encoding,omitempty"`
}

// Event 是一条时间线事件。Version 只在 header 事件上填充。
type Event struct {
	Kind     EventKind `json:"kind"`
	Version  int       `json:"version,omitempty"`
	AtMS     int64     `json:"at_ms,omitempty"`
	Data     string    `json:"data,omitempty"` // output: base64；resize: 不用
	Cols     int       `json:"cols,omitempty"`
	Rows     int       `json:"rows,omitempty"`
	Encoding string    `json:"encoding,omitempty"`
}

var (
	ErrInvalidHeader = errors.New("timeline: invalid or missing header")
	ErrCorruptEvent  = errors.New("timeline: corrupt event")
	ErrWrongVersion  = errors.New("timeline: unsupported version")
)

// Recorder 把写入的字节按时间线记录到 JSONL。
type Recorder struct {
	w      io.WriteCloser
	start  time.Time
	clock  func() time.Time
	lastAt int64
	closed bool
}

// NewRecorder 创建录制器，写入 path。header 的 cols/rows 由调用方给出。
func NewRecorder(path string, cols, rows int) (*Recorder, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	r := &Recorder{w: f, start: time.Now(), clock: time.Now}
	if err := r.write(Event{Kind: KindHeader, Version: formatVersion, Cols: cols, Rows: rows, Encoding: "utf-8"}); err != nil {
		f.Close()
		return nil, err
	}
	return r, nil
}

// setClock 注入时钟（测试用）；必须在首次 Write 前调用。
func (r *Recorder) setClock(fn func() time.Time) {
	if fn != nil {
		r.clock = fn
		r.start = fn()
	}
}

// write 内部写一行事件。Header 事件的 Version 放在 Event 顶层而非 Header——
// 这里用一个专用结构保证第一行同时含 kind 与 header 字段。
func (r *Recorder) write(ev Event) error {
	line, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = r.w.Write(append(line, '\n'))
	return err
}

// Write 实现 io.Writer：把 p 作为一条 output 事件记录（base64 编码）。
func (r *Recorder) Write(p []byte) (int, error) {
	if r.closed {
		return 0, os.ErrClosed
	}
	at := r.clock().Sub(r.start).Milliseconds()
	if at < r.lastAt {
		at = r.lastAt // 单调
	}
	r.lastAt = at
	ev := Event{Kind: KindOutput, AtMS: at, Data: base64.StdEncoding.EncodeToString(p)}
	if err := r.write(ev); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Resize 记录一次终端尺寸变化。
func (r *Recorder) Resize(cols, rows int) error {
	if r.closed {
		return os.ErrClosed
	}
	at := r.clock().Sub(r.start).Milliseconds()
	return r.write(Event{Kind: KindResize, AtMS: at, Cols: cols, Rows: rows})
}

// Close 关闭录制器。
func (r *Recorder) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	return r.w.Close()
}

// Player 按时间线回放事件。
type Player struct {
	events []Event
	header Header
}

// Load 从 path 加载录制，校验头与版本。
func Load(path string) (*Player, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	var events []Event
	first := true
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrCorruptEvent, err)
		}
		if first {
			first = false
			if ev.Kind != KindHeader {
				return nil, ErrInvalidHeader
			}
		}
		events = append(events, ev)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, ErrInvalidHeader
	}
	h := events[0]
	if h.Version != formatVersion {
		return nil, fmt.Errorf("%w: got %d, want %d", ErrWrongVersion, h.Version, formatVersion)
	}
	return &Player{
		events: events[1:],
		header: Header{Version: h.Version, Cols: h.Cols, Rows: h.Rows, Encoding: h.Encoding},
	}, nil
}

// Header 返回录制头。
func (p *Player) Header() Header { return p.header }

// Events 返回事件快照（不含 header）。
func (p *Player) Events() []Event {
	out := make([]Event, len(p.events))
	copy(out, p.events)
	return out
}

// Replay 按时间线回放：每个 output 事件在 at_ms 到达时写入 w；resize 通过
// onResize 回调通知（可为 nil）。若 realtime=false，则不等真实时间，立即回放。
func (p *Player) Replay(w io.Writer, realtime bool, onResize func(cols, rows int)) error {
	var last int64
	var lastWall time.Time
	for _, ev := range p.events {
		if realtime && ev.AtMS > last {
			wait := time.Duration(ev.AtMS-last) * time.Millisecond
			elapsed := time.Since(lastWall)
			if wait > elapsed {
				time.Sleep(wait - elapsed)
			}
		}
		switch ev.Kind {
		case KindOutput:
			data, err := base64.StdEncoding.DecodeString(ev.Data)
			if err != nil {
				return fmt.Errorf("%w: bad base64: %v", ErrCorruptEvent, err)
			}
			if _, err := w.Write(data); err != nil {
				return err
			}
		case KindResize:
			if onResize != nil {
				onResize(ev.Cols, ev.Rows)
			}
		}
		last = ev.AtMS
		lastWall = time.Now()
	}
	return nil
}

// Snapshot 返回所有 output 事件拼接后的最终字节（用于帧哈希对比，不等待时间）。
func (p *Player) Snapshot() ([]byte, error) {
	var out []byte
	for _, ev := range p.events {
		if ev.Kind != KindOutput {
			continue
		}
		data, err := base64.StdEncoding.DecodeString(ev.Data)
		if err != nil {
			return nil, fmt.Errorf("%w: bad base64: %v", ErrCorruptEvent, err)
		}
		out = append(out, data...)
	}
	return out, nil
}
