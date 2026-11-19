package tui

import (
	"strings"
	"testing"
)

func TestUnchangedFrameWritesNoBytes(t *testing.T) {
	m := New(Config{})
	buffer := NewBuffer(80, 24)
	var out strings.Builder
	fw := &frameWriter{}

	if err := fw.draw(m, buffer, &out, true); err != nil {
		t.Fatalf("first draw: %v", err)
	}
	if out.Len() == 0 {
		t.Fatal("the first frame must paint the screen and the caret")
	}
	if !strings.Contains(out.String(), showCursor) {
		t.Fatalf("the first frame must show the caret: %q", out.String())
	}

	out.Reset()
	if err := fw.draw(m, buffer, &out, false); err != nil {
		t.Fatalf("second draw: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("an unchanged second frame wrote %d bytes: %q", out.Len(), out.String())
	}
}

func TestFrameGateRepaintsAfterStateChange(t *testing.T) {
	m := New(Config{})
	buffer := NewBuffer(80, 24)
	var out strings.Builder
	fw := &frameWriter{}
	if err := fw.draw(m, buffer, &out, true); err != nil {
		t.Fatalf("first draw: %v", err)
	}

	out.Reset()
	m.SetStatus("something changed")
	if err := fw.draw(m, buffer, &out, false); err != nil {
		t.Fatalf("second draw: %v", err)
	}
	if out.Len() == 0 {
		t.Fatal("a status change must repaint the status row")
	}
	if !strings.Contains(out.String(), "something changed") {
		t.Fatalf("the new status is missing from the repaint: %q", out.String())
	}
}

func TestFrameGateForcesDrawAfterResize(t *testing.T) {
	m := New(Config{})
	buffer := NewBuffer(80, 24)
	var out strings.Builder
	fw := &frameWriter{}
	if err := fw.draw(m, buffer, &out, true); err != nil {
		t.Fatalf("first draw: %v", err)
	}

	buffer.Resize(100, 30)
	buffer.Invalidate()
	out.Reset()
	if err := fw.draw(m, buffer, &out, true); err != nil {
		t.Fatalf("resize draw: %v", err)
	}
	if !strings.Contains(out.String(), showCursor) {
		t.Fatalf("the caret must be repositioned after a resize: %q", out.String())
	}
	if !strings.Contains(out.String(), cursorTo(1, 1)) {
		t.Fatalf("a full repaint is expected after Invalidate: %q", out.String())
	}
}
