package tui

import (
	"strings"
	"testing"
	"time"
)

// fixedFrameClock 是帧键测试的确定性时间源。固定
// now 和 started 使头部时钟保持稳定，这样只有被测的变更
// 能改变键。
func fixedFrameClock() time.Time {
	return time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
}

// seededFrameModel 返回一个带长记录的已绘制模型，使较小的
// Scroll 就能移动视口和状态行滚动标记。
func seededFrameModel() (*Model, frameCacheKey) {
	m := New(Config{})
	base := fixedFrameClock()
	m.now = func() time.Time { return base }
	m.started = base
	m.AppendUser(strings.Repeat("line of transcript\n", 50))
	m.Frame(80, 24)
	return m, newFrameCacheKey(m, 80, 24)
}

// TestFrameCacheKeyIncludesPendingDanger 在弹窗
// 加入 danger 原因时保持缓存诚实：两个其他方面相同但 danger
// 文本不同的帧绝不能共享缓存的绘制结果。
func TestFrameCacheKeyIncludesPendingDanger(t *testing.T) {
	m := New(Config{})
	m.now = func() time.Time { return fixedFrameClock() }
	m.started = fixedFrameClock()

	m.pending = &approvalRequest{name: "bash", args: "{}", danger: "rm"}
	before := newFrameCacheKey(m, 80, 24)
	m.pending = &approvalRequest{name: "bash", args: "{}", danger: "sudo rm -rf /"}
	after := newFrameCacheKey(m, 80, 24)

	if frameCacheHit(before, after) {
		t.Fatal("a changed pending danger must miss the frame cache")
	}
	if isPureScrollChange(before, after) {
		t.Fatal("a changed pending danger is not a pure scroll change")
	}
}

func TestFrameCacheKeyHitAndMiss(t *testing.T) {
	tests := []struct {
		name           string
		mutate         func(*Model)
		wantHit        bool
		wantPureScroll bool
	}{
		{"unchanged", func(*Model) {}, true, false},
		{"scroll_only", func(m *Model) { m.Scroll(2) }, false, true},
		{
			"content_version",
			func(m *Model) { m.AppendUser("new entry") },
			false, false,
		},
		{"usage", func(m *Model) { m.SetUsage(10, 20) }, false, false},
		{"cached_tokens", func(m *Model) { m.SetCachedTokens(5) }, false, false},
		{
			"context_info",
			func(m *Model) { m.SetContextInfo(1000, "$1") },
			false, false,
		},
		{"usage_visible", func(m *Model) { m.SetUsageVisible(true) }, false, false},
		{"status_is_time_sensitive", func(m *Model) { m.SetStatus("hi") }, false, false},
		{"selection", func(m *Model) { m.MouseDown(3, 4) }, false, false},
		{"theme", func(m *Model) { _ = m.SetTheme(ThemeLight) }, false, false},
		{"no_color", func(m *Model) { m.noColor = true }, false, false},
		{
			"panel_scroll",
			func(m *Model) { m.showTasks = true; m.tasksScroll = 2 },
			false, false,
		},
		{
			"approval_modal",
			func(m *Model) { m.pending = &approvalRequest{name: "sh", args: "{}"} },
			false, false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, before := seededFrameModel()
			tc.mutate(m)
			after := newFrameCacheKey(m, 80, 24)
			if got := frameCacheHit(before, after); got != tc.wantHit {
				t.Fatalf("frameCacheHit(%s) = %v, want %v\nbefore=%+v\nafter=%+v",
					tc.name, got, tc.wantHit, before, after)
			}
			if got := isPureScrollChange(before, after); got != tc.wantPureScroll {
				t.Fatalf("isPureScrollChange(%s) = %v, want %v\nbefore=%+v\nafter=%+v",
					tc.name, got, tc.wantPureScroll, before, after)
			}
		})
	}
}

func TestFrameCacheKeyGeometry(t *testing.T) {
	m, base := seededFrameModel()
	for _, tc := range []struct {
		name           string
		w, h           int
		wantHit        bool
		wantPureScroll bool
	}{
		{"same", 80, 24, true, false},
		{"width", 100, 24, false, false},
		{"height", 80, 30, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cur := newFrameCacheKey(m, tc.w, tc.h)
			if got := frameCacheHit(base, cur); got != tc.wantHit {
				t.Fatalf("frameCacheHit = %v, want %v", got, tc.wantHit)
			}
			if got := isPureScrollChange(base, cur); got != tc.wantPureScroll {
				t.Fatalf("isPureScrollChange = %v, want %v", got, tc.wantPureScroll)
			}
		})
	}
}

func TestFrameCacheKeyTimeSensitivity(t *testing.T) {
	base := fixedFrameClock()
	clock := base
	newModel := func() *Model {
		m := New(Config{})
		m.now = func() time.Time { return clock }
		m.started = base
		return m
	}

	t.Run("idle_within_second_is_reusable", func(t *testing.T) {
		clock = base
		m := newModel()
		before := newFrameCacheKey(m, 80, 24)
		clock = base.Add(900 * time.Millisecond)
		after := newFrameCacheKey(m, 80, 24)
		if !frameCacheHit(before, after) {
			t.Fatal("idle frames inside one clock second should be reusable")
		}
		if after.timeSensitive {
			t.Fatal("idle frame must not be time-sensitive")
		}
	})

	t.Run("idle_across_second_is_not_reusable", func(t *testing.T) {
		clock = base
		m := newModel()
		before := newFrameCacheKey(m, 80, 24)
		clock = base.Add(time.Second)
		after := newFrameCacheKey(m, 80, 24)
		if frameCacheHit(before, after) {
			t.Fatal("the header clock changed; the frame must be repainted")
		}
		if isPureScrollChange(before, after) {
			t.Fatal("a clock tick is not a scroll change")
		}
	})

	t.Run("spinner_forbids_reuse", func(t *testing.T) {
		clock = base
		m := newModel()
		m.working = true
		before := newFrameCacheKey(m, 80, 24)
		clock = base.Add(time.Millisecond)
		after := newFrameCacheKey(m, 80, 24)
		if !before.timeSensitive || !after.timeSensitive {
			t.Fatal("a working spinner must be marked time-sensitive")
		}
		if frameCacheHit(before, after) {
			t.Fatal("animated frames must never compare equal at different instants")
		}
	})
}

func TestFrameCacheKeyEqualKeyPaintsEqualBytes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		noColor bool
	}{
		{"default_theme", false},
		{"no_color", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := New(Config{NoColor: tc.noColor})
			base := fixedFrameClock()
			m.now = func() time.Time { return base }
			m.started = base
			m.AppendUser("user text")
			m.AppendTool("sh", "{}", "ok", false)

			first := m.Frame(80, 24)
			var out1 strings.Builder
			if _, err := first.Flush(&out1); err != nil {
				t.Fatal(err)
			}
			key1 := newFrameCacheKey(m, 80, 24)

			second := m.Frame(80, 24)
			var out2 strings.Builder
			if _, err := second.Flush(&out2); err != nil {
				t.Fatal(err)
			}
			key2 := newFrameCacheKey(m, 80, 24)

			if out1.Len() == 0 {
				t.Fatal("the default frame must not be empty")
			}
			if !frameCacheHit(key1, key2) {
				t.Fatal("two paints of unchanged state must share a key")
			}
			if out1.String() != out2.String() {
				t.Fatal("equal keys must produce byte-identical frames")
			}
		})
	}
}

// TestPureScrollRepaintIsRequired 记录为何 draw 在
// 仅滚动的帧上不能跳过 Paint：正文窗口和状态行 "↑N" 标记都会移动，
// 因此即使 version 未变，发出的字节也确实改变。
func TestPureScrollRepaintIsRequired(t *testing.T) {
	m, before := seededFrameModel()
	first := m.Frame(80, 24)
	var out1 strings.Builder
	if _, err := first.Flush(&out1); err != nil {
		t.Fatal(err)
	}

	m.Scroll(2)
	after := newFrameCacheKey(m, 80, 24)
	if !isPureScrollChange(before, after) {
		t.Fatal("this mutation should be a pure scroll change")
	}
	if frameCacheHit(before, after) {
		t.Fatal("a scroll change must be a cache miss")
	}

	second := m.Frame(80, 24)
	var out2 strings.Builder
	if _, err := second.Flush(&out2); err != nil {
		t.Fatal(err)
	}
	if out1.String() == out2.String() {
		t.Fatal("scrolling must repaint the shifted body and the scroll marker")
	}
	if !strings.Contains(out2.String(), "↑2") {
		t.Fatalf("the status line must show the new scroll offset: %q", out2.String())
	}
}
