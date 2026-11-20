package tui

import (
	"strings"
	"testing"
)

func TestFlushWritesEveryRowOnTheFirstFrame(t *testing.T) {
	buf := NewBuffer(10, 3)
	buf.Clear()
	buf.WriteString(0, 0, 10, "hello", Style{})

	var out strings.Builder
	written, err := buf.Flush(&out)
	if err != nil {
		t.Fatalf("flush: %v", err)
	}
	if written != 3 {
		t.Fatalf("full frame wrote %d rows, want 3", written)
	}
	if !strings.Contains(out.String(), "\x1b[1;1H") {
		t.Fatalf("frame is missing the absolute cursor move: %q", out.String())
	}
	if !strings.Contains(out.String(), "hello") {
		t.Fatalf("frame is missing the text: %q", out.String())
	}
}

func TestFlushOnlyRedrawsChangedRows(t *testing.T) {
	buf := NewBuffer(10, 4)
	buf.Clear()
	buf.WriteString(0, 0, 10, "header", Style{})
	var sink strings.Builder
	if _, err := buf.Flush(&sink); err != nil {
		t.Fatalf("first flush: %v", err)
	}

	sink.Reset()
	if written, _ := buf.Flush(&sink); written != 0 {
		t.Fatalf("unchanged frame wrote %d rows, want 0", written)
	}

	buf.Set(0, 2, "x", Style{})
	sink.Reset()
	written, _ := buf.Flush(&sink)
	if written != 1 {
		t.Fatalf("one changed row wrote %d rows, want 1", written)
	}
	if !strings.Contains(sink.String(), "\x1b[3;1H") {
		t.Fatalf("expected the changed row to be addressed, got %q", sink.String())
	}
	if strings.Contains(sink.String(), "header") {
		t.Fatalf("unchanged rows must not be repainted: %q", sink.String())
	}
}

func TestResizeForcesAFullRepaint(t *testing.T) {
	buf := NewBuffer(6, 2)
	buf.Clear()
	buf.WriteString(0, 0, 6, "abc", Style{})
	var sink strings.Builder
	buf.Flush(&sink)

	buf.Resize(6, 4)
	buf.Clear()
	sink.Reset()
	if written, _ := buf.Flush(&sink); written != 4 {
		t.Fatalf("resize wrote %d rows, want 4", written)
	}
}

func TestWideRuneTakesTwoCells(t *testing.T) {
	buf := NewBuffer(6, 1)
	buf.Clear()
	buf.Set(0, 0, "你", Style{})
	if got := buf.RowText(0); got != "你" {
		t.Fatalf("row = %q, want 你", got)
	}
	if buf.cells[1].Text != "" {
		t.Fatalf("second cell should be the wide-rune continuation, got %q", buf.cells[1].Text)
	}
	if used := buf.WriteString(0, 0, 1, "你", Style{}); used != 0 {
		t.Fatalf("a wide rune must not be written into a one-cell gap, used %d", used)
	}
}

func TestRowTextSkipsContinuationCells(t *testing.T) {
	buf := NewBuffer(4, 1)
	buf.Clear()
	buf.WriteString(0, 0, 4, "你a", Style{})
	if got := buf.RowText(0); got != "你a" {
		t.Fatalf("row = %q, want 你a", got)
	}
}

func TestWriteStringKeepsGraphemeClustersWhole(t *testing.T) {
	// 布局把 ZWJ 家庭 emoji 测为两个单元格（TextWidth），因此缓冲区
	// 必须恰好绘制这两个单元格，并把该簇的每个字节 ——
	// 包括不可见的 ZWJ —— 保留在绘制输出中。按 rune 遍历
	// 过去会丢掉 ZWJ 并预留六个单元格，把字形撕开，
	// 并使后面的 span 向右多推三个单元格。
	for _, tc := range []struct {
		name, text string
	}{
		{"ZWJ family", familyZWJ},
		{"flag pair", flagCN},
		{"skin tone", thumbTone},
		{"combining mark", combiningE},
	} {
		t.Run(tc.name, func(t *testing.T) {
			painted := "A" + tc.text + "B"
			buf := NewBuffer(TextWidth(painted), 1)
			buf.Clear()
			used := buf.WriteString(0, 0, buf.Width(), painted, Style{})
			if want := TextWidth(painted); used != want {
				t.Fatalf("WriteString used %d cells, want %d", used, want)
			}
			if got := buf.RowText(0); got != painted {
				t.Fatalf("RowText = %q, want %q", got, painted)
			}
			var out strings.Builder
			if _, err := buf.Flush(&out); err != nil {
				t.Fatalf("flush: %v", err)
			}
			if !strings.Contains(out.String(), tc.text) {
				t.Fatalf("flushed frame lost the grapheme cluster: %q", out.String())
			}
		})
	}
}

func TestFlushEmitsStyleChangesAndClearsTheLine(t *testing.T) {
	buf := NewBuffer(8, 1)
	buf.Clear()
	buf.WriteString(0, 0, 8, "hi", Style{Fg: ColorGreen, Bold: true})
	var out strings.Builder
	buf.Flush(&out)
	if !strings.Contains(out.String(), "32") || !strings.Contains(out.String(), "1") {
		t.Fatalf("expected a green bold SGR, got %q", out.String())
	}
	if !strings.Contains(out.String(), "\x1b[K") {
		t.Fatalf("expected the row to end with EL, got %q", out.String())
	}
}
