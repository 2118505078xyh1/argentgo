package tui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// 本文件覆盖 PromptStore 接进 TUI 输入环后的行为：提交写盘、重启恢复、
// 加载只恢复内存不回写、默认关闭逐字节不变，以及写盘错误的吞掉。

func TestPromptHistoryPersistsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prompts.jsonl")
	store := NewPromptStore(path)
	m := New(Config{Locale: "en"})
	m.SetPromptStore(store)

	submit := func(text string) {
		typeBytes(m, text+"\r")
		m.EndTurn(nil)
	}
	submit("first prompt")
	submit("你好，世界 😀") // CJK 与 emoji 必须按 rune 完整写盘、完整召回。

	want := []string{"first prompt", "你好，世界 😀"}
	if got := store.Recent(10); !slices.Equal(got, want) {
		t.Fatalf("Recent = %q, want %q", got, want)
	}

	// 重启：新的 store + 新的 model 经 SetPromptStore 恢复历史。
	restarted := New(Config{Locale: "en"})
	restarted.SetPromptStore(NewPromptStore(path))

	// Recent 保序旧→新、索引 -1，因此第一次 Up 取最新一条。
	restarted.HandleKey(Key{Kind: KindUp})
	if got := restarted.Input(); got != "你好，世界 😀" {
		t.Fatalf("first Up = %q, want 最新一条", got)
	}
	restarted.HandleKey(Key{Kind: KindUp})
	if got := restarted.Input(); got != "first prompt" {
		t.Fatalf("second Up = %q, want 最旧一条", got)
	}
	restarted.HandleKey(Key{Kind: KindDown})
	if got := restarted.Input(); got != "你好，世界 😀" {
		t.Fatalf("Down = %q, want 回到最新一条", got)
	}
}

func TestSeedHistoryDoesNotWriteDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prompts.jsonl")
	e := NewEditor()
	e.SeedHistory([]string{"older", "newer"})

	// SeedHistory 只灌内存环：既没调用 Append，也不创建文件。
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("SeedHistory 触碰了文件: err=%v", err)
	}
	if !e.HistoryPrev() || e.Text() != "newer" {
		t.Fatalf("first HistoryPrev = %q, want newer", e.Text())
	}
	if !e.HistoryPrev() || e.Text() != "older" {
		t.Fatalf("second HistoryPrev = %q, want older", e.Text())
	}
	if !e.HistoryNext() || e.Text() != "newer" {
		t.Fatalf("HistoryNext = %q, want newer", e.Text())
	}

	// 超过 200 条时只保留最新的 200 条，顺序仍为旧→新。
	many := make([]string, 205)
	for i := range many {
		many[i] = "item"
	}
	many[204] = "tail"
	e.SeedHistory(many)
	if len(e.history) != 200 || e.history[199] != "tail" {
		t.Fatalf("cap 后 history len=%d tail=%q", len(e.history), e.history[len(e.history)-1])
	}
}

func TestRecordSwallowsAppendError(t *testing.T) {
	m := New(Config{Locale: "en"})
	// 父目录不存在：Append 必然失败，但内存历史仍更新，且不向屏幕发布通知。
	m.SetPromptStore(NewPromptStore(filepath.Join(t.TempDir(), "missing", "prompts.jsonl")))
	typeBytes(m, "still works\r")
	if got := m.TakeSubmitted(); got != "still works" {
		t.Fatalf("submitted = %q", got)
	}
	if len(m.editor.history) != 1 || m.editor.history[0] != "still works" {
		t.Fatalf("history = %q, want [still works]", m.editor.history)
	}
	if got := m.notices.Len(); got != 0 {
		t.Fatalf("写盘失败不应发布通知，got %d", got)
	}
}

func TestPromptHistoryDisabledByteForByteBaseline(t *testing.T) {
	base := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	newModel := func() *Model {
		m := New(Config{Locale: "en"})
		m.started = base
		m.now = func() time.Time { return base }
		return m
	}
	plain := newModel()
	off := newModel()
	off.SetPromptStore(nil)

	// 首帧逐字节一致。
	if got, want := frameBytes(off, 80, 24), frameBytes(plain, 80, 24); got != want {
		t.Fatalf("opt-in 关闭时首帧与基线不一致:\noff:   %q\nplain: %q", got, want)
	}

	// 历史导航与 Up 行为一致：两条提交后第一次 Up 召回最新一条。
	drive := func(m *Model) {
		typeBytes(m, "one\r")
		m.EndTurn(nil)
		typeBytes(m, "two\r")
		m.EndTurn(nil)
		m.HandleKey(Key{Kind: KindUp})
	}
	drive(plain)
	drive(off)
	if got := off.Input(); got != "two" {
		t.Fatalf("Up = %q, want two", got)
	}
	if got, want := frameBytes(off, 80, 24), frameBytes(plain, 80, 24); got != want {
		t.Fatalf("opt-in 关闭时历史帧与基线不一致:\noff:   %q\nplain: %q", got, want)
	}
	if off.editor.store != nil {
		t.Fatalf("opt-in 关闭时 store 应为 nil，got %v", off.editor.store)
	}
}

func TestRunWiresPromptHistoryOption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prompts.jsonl")
	if err := NewPromptStore(path).Append("seeded from disk"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	reader, writer := io.Pipe()
	defer writer.Close()
	defer reader.Close()
	var out bytes.Buffer
	ready := make(chan *Model, 1)
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	opts := Options{
		IO:            &IO{In: reader, Out: &out},
		PromptHistory: NewPromptStore(path),
		OnReady:       func(m *Model) { ready <- m },
	}
	go func() { done <- Run(ctx, opts) }()

	var m *Model
	select {
	case m = <-ready:
	case <-time.After(5 * time.Second):
		t.Fatalf("Run never became ready")
	}

	// OnReady 在第一帧之后触发，此时 Run 应已把 PromptHistory 装到编辑器。
	m.HandleKey(Key{Kind: KindUp})
	if got := m.Input(); got != "seeded from disk" {
		t.Fatalf("Run 未接线 PromptHistory: Up = %q", got)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("Run did not return after cancel")
	}
}
