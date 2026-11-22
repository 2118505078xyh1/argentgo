package tui

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// wireSpyWriter 是注入 draw 的 spy，记录实际写出的字节，
// 用来断言缓存命中时第二帧一个字节都不再写。
type wireSpyWriter struct {
	buf bytes.Buffer
}

func (w *wireSpyWriter) Write(p []byte) (int, error) { return w.buf.Write(p) }
func (w *wireSpyWriter) reset()                      { w.buf.Reset() }
func (w *wireSpyWriter) len() int                    { return w.buf.Len() }
func (w *wireSpyWriter) String() string              { return w.buf.String() }

// frameWireModel 建一个时钟固定、已完成一次 Paint 的模型，
// 使布局与滚动状态稳定，后续键比较只反映被测的状态变化。
func frameWireModel() *Model {
	m := New(Config{})
	base := fixedFrameClock()
	m.now = func() time.Time { return base }
	m.started = base
	m.Frame(80, 24)
	return m
}

// frameWireModelWithClock 与 frameWireModel 相同，但把时钟暴露给
// 调用方推进，用于验证 spinner 随时间的重绘。
func frameWireModelWithClock(clock *time.Time) *Model {
	m := New(Config{})
	base := fixedFrameClock()
	*clock = base
	m.now = func() time.Time { return *clock }
	m.started = base
	m.Frame(80, 24)
	return m
}

// rowContains 检查任一屏幕行是否含 text。Paint 按整行重绘宽字素，
// 因此缓存命中后该行内容必须保持完整，不能被拆成半个 CJK 字形。
func rowContains(b *Buffer, text string) bool {
	for y := 0; y < b.Height(); y++ {
		if strings.Contains(b.RowText(y), text) {
			return true
		}
	}
	return false
}

// TestFrameCacheEnabledFromEnv 固定 opt-in 语义：默认关闭，
// 只有精确读到 "1" 才打开。
func TestFrameCacheEnabledFromEnv(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  func(string) string
		want bool
	}{
		{"nil_env", nil, false},
		{"unset", func(string) string { return "" }, false},
		{"one", func(string) string { return "1" }, true},
		{"zero", func(string) string { return "0" }, false},
		{"true_word", func(string) string { return "true" }, false},
		{"other_key", func(k string) string {
			if k == envFrameCache {
				return ""
			}
			return "1"
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := frameCacheEnabledFromEnv(tc.env); got != tc.want {
				t.Fatalf("frameCacheEnabledFromEnv = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestFrameCacheWireOnSkipsUnchangedPaint 是核心断言：开启缓存后，
// 相同状态 + tick（仅 MarkDirty）的第二帧跳过 Paint（dirty 仍为真）
// 且 spy 收到 0 字节。
func TestFrameCacheWireOnSkipsUnchangedPaint(t *testing.T) {
	m := frameWireModel()
	buffer := NewBuffer(80, 24)
	spy := &wireSpyWriter{}
	fw := &frameWriter{cacheEnabled: true}

	if err := fw.draw(m, buffer, spy, true); err != nil {
		t.Fatalf("first draw: %v", err)
	}
	if spy.len() == 0 {
		t.Fatal("the first frame must write bytes")
	}
	if m.Dirty() {
		t.Fatal("the first frame must Paint and clear dirty")
	}

	m.MarkDirty() // 模拟 tick：只标脏，视觉状态未变
	spy.reset()
	if err := fw.draw(m, buffer, spy, false); err != nil {
		t.Fatalf("second draw: %v", err)
	}
	if spy.len() != 0 {
		t.Fatalf("cached frame wrote %d bytes: %q", spy.len(), spy.String())
	}
	if !m.Dirty() {
		t.Fatal("Paint was called on a cache hit (dirty was cleared)")
	}
}

// TestFrameCacheWireOffKeepsLegacyPaint 锁定关闭基线：
// 未 opt-in 时每个脏帧仍无条件 Paint，与引入缓存前逐字节一致。
func TestFrameCacheWireOffKeepsLegacyPaint(t *testing.T) {
	m := frameWireModel()
	buffer := NewBuffer(80, 24)
	spy := &wireSpyWriter{}
	fw := &frameWriter{} // cacheEnabled 默认 false

	if err := fw.draw(m, buffer, spy, true); err != nil {
		t.Fatalf("first draw: %v", err)
	}
	m.MarkDirty()
	spy.reset()
	if err := fw.draw(m, buffer, spy, false); err != nil {
		t.Fatalf("second draw: %v", err)
	}
	if m.Dirty() {
		t.Fatal("cache disabled: every dirty frame must still Paint")
	}
	if spy.len() != 0 {
		t.Fatalf("unchanged frame should still write 0 bytes, got %d", spy.len())
	}
	if fw.haveKey || fw.lastKey != (frameCacheKey{}) {
		t.Fatal("cache disabled: draw must not touch the key state")
	}
}

// TestFrameCacheWireForceRepaints 验证 force 强制走 Paint：
// 即使上一帧键命中，force 也必须重绘并在 Invalidate 后写出整屏。
func TestFrameCacheWireForceRepaints(t *testing.T) {
	m := frameWireModel()
	buffer := NewBuffer(80, 24)
	spy := &wireSpyWriter{}
	fw := &frameWriter{cacheEnabled: true}

	if err := fw.draw(m, buffer, spy, true); err != nil {
		t.Fatalf("first draw: %v", err)
	}
	m.MarkDirty()
	if err := fw.draw(m, buffer, spy, false); err != nil {
		t.Fatalf("cached draw: %v", err)
	}
	if !m.Dirty() {
		t.Fatal("setup: a cache hit should leave dirty set")
	}

	buffer.Invalidate() // 让整屏重绘可见
	spy.reset()
	if err := fw.draw(m, buffer, spy, true); err != nil {
		t.Fatalf("forced draw: %v", err)
	}
	if m.Dirty() {
		t.Fatal("force must call Paint and clear dirty")
	}
	if spy.len() == 0 {
		t.Fatal("force after Invalidate must repaint the whole screen")
	}
}

// TestFrameCacheWireSpinnerForcesRepaint 验证 spinner 状态变化与
// 时间推进都会令键失效，从而正确重绘，不会定格动画帧。
func TestFrameCacheWireSpinnerForcesRepaint(t *testing.T) {
	var clock time.Time
	m := frameWireModelWithClock(&clock)
	buffer := NewBuffer(80, 24)
	spy := &wireSpyWriter{}
	fw := &frameWriter{cacheEnabled: true}

	if err := fw.draw(m, buffer, spy, true); err != nil {
		t.Fatalf("first draw: %v", err)
	}

	m.working = true // 打开 spinner：键从非时间敏感变为时间敏感
	clock = clock.Add(time.Millisecond)
	m.MarkDirty()
	spy.reset()
	if err := fw.draw(m, buffer, spy, false); err != nil {
		t.Fatalf("spinner draw: %v", err)
	}
	if m.Dirty() {
		t.Fatal("a spinner state change must repaint")
	}

	// spinner 存活时，仅推进时间也不得复用上一帧。
	clock = clock.Add(time.Millisecond)
	m.MarkDirty()
	spy.reset()
	if err := fw.draw(m, buffer, spy, false); err != nil {
		t.Fatalf("spinner tick draw: %v", err)
	}
	if m.Dirty() {
		t.Fatal("a live spinner must repaint on every distinct instant")
	}
}

// TestFrameCacheWireNoticeForcesRepaint 验证通知出现让键失效并重绘。
func TestFrameCacheWireNoticeForcesRepaint(t *testing.T) {
	m := frameWireModel()
	buffer := NewBuffer(80, 24)
	spy := &wireSpyWriter{}
	fw := &frameWriter{cacheEnabled: true}

	if err := fw.draw(m, buffer, spy, true); err != nil {
		t.Fatalf("first draw: %v", err)
	}

	m.SetStatus("注意：磁盘不足") // 通知栈与旧式状态同时变为存活
	spy.reset()
	if err := fw.draw(m, buffer, spy, false); err != nil {
		t.Fatalf("notice draw: %v", err)
	}
	if m.Dirty() {
		t.Fatal("a new notice must repaint")
	}
	if !strings.Contains(spy.String(), "注意") {
		t.Fatalf("the notice text is missing from the repaint: %q", spy.String())
	}
}

// TestFrameCacheWireCJK 验证 CJK 宽字素在缓存命中时整行保持完整，
// 内容变化时又按整行重绘、不撕裂字形。
func TestFrameCacheWireCJK(t *testing.T) {
	var clock time.Time
	m := frameWireModelWithClock(&clock)
	m.AppendUser("你好，世界")
	buffer := NewBuffer(80, 24)
	spy := &wireSpyWriter{}
	fw := &frameWriter{cacheEnabled: true}

	if err := fw.draw(m, buffer, spy, true); err != nil {
		t.Fatalf("first draw: %v", err)
	}
	if !strings.Contains(spy.String(), "你好，世界") {
		t.Fatalf("the CJK text is missing from the first frame: %q", spy.String())
	}

	// 命中：0 字节，且屏幕行里的 CJK 整块内容没有被重绘拆坏。
	m.MarkDirty()
	spy.reset()
	if err := fw.draw(m, buffer, spy, false); err != nil {
		t.Fatalf("cached draw: %v", err)
	}
	if spy.len() != 0 {
		t.Fatalf("cached CJK frame wrote %d bytes", spy.len())
	}
	if !rowContains(buffer, "你好，世界") {
		t.Fatal("the cached CJK row must remain intact")
	}

	// 内容变化：键未命中，重绘必须写出新的 CJK 文本。
	m.AppendUser("世界，你好")
	spy.reset()
	if err := fw.draw(m, buffer, spy, false); err != nil {
		t.Fatalf("changed draw: %v", err)
	}
	if m.Dirty() {
		t.Fatal("a CJK content change must repaint")
	}
	if !strings.Contains(spy.String(), "世界，你好") {
		t.Fatalf("the new CJK text is missing from the repaint: %q", spy.String())
	}
}
