package tui

// This file is deliberately self-contained: it factors the transcript scroll
// arithmetic into pure helpers that can be tested without a Model, a Buffer or
// a terminal. Wiring them into model_mutate.go / render.go is left to the
// owners of those files.
//
// The transcript scroll offset is bottom-relative: 0 means "newest line
// visible", larger values mean "scrolled further back". render.go derives the
// visible row range from len(body)-m.scroll, so a value above
// len(body)-bodyH has no visual effect but still has to be undone by later
// PageDown presses. Clamping both bounds at input time is what keeps a large
// session from feeling stuck at the top after repeated wheel/page scrolling.

const (
	maxScrollInt = int(^uint(0) >> 1)
	minScrollInt = -maxScrollInt - 1
)

// saturatingScrollAdd adds two offsets without wrapping around on extreme
// inputs, so callers can clamp the result safely afterwards.
func saturatingScrollAdd(a, b int) int {
	if b > 0 && a > maxScrollInt-b {
		return maxScrollInt
	}
	if b < 0 && a < minScrollInt-b {
		return minScrollInt
	}
	return a + b
}

// transcriptScrollMax is the largest meaningful bottom-relative offset for a
// transcript of totalLines rows shown in a viewport of viewportHeight rows.
// A transcript that fits, or a non-positive total/viewport, has no scrollable
// range at all.
func transcriptScrollMax(totalLines, viewportHeight int) int {
	if totalLines <= 0 || viewportHeight <= 0 {
		return 0
	}
	max := totalLines - viewportHeight
	if max < 0 {
		return 0
	}
	return max
}

// clampTranscriptScroll brings a possibly stale offset back into
// [0, transcriptScrollMax], e.g. after the transcript shrinks or the terminal
// is resized taller.
func clampTranscriptScroll(offset, totalLines, viewportHeight int) int {
	max := transcriptScrollMax(totalLines, viewportHeight)
	if offset <= 0 {
		return 0
	}
	if offset > max {
		return max
	}
	return offset
}

// scrollTranscript applies one scroll step and clamps in the same operation.
// Positive delta scrolls back toward older output, negative delta scrolls
// toward the newest line. Clamping here (rather than only at paint time)
// prevents overscroll from accumulating: one PageDown undoes one PageUp even
// after dozens of wheel ticks past the oldest line.
func scrollTranscript(offset, delta, totalLines, viewportHeight int) int {
	return clampTranscriptScroll(saturatingScrollAdd(offset, delta), totalLines, viewportHeight)
}

// transcriptScrollAfterAppend returns the offset after new lines arrive. When
// pinnedToBottom is true the view follows the stream and stays at 0.
// Otherwise the bottom-relative offset grows by appendedLines so the same
// absolute rows stay on screen, then is clamped to the new bounds. A
// non-positive appendedLines only re-clamps the current offset.
func transcriptScrollAfterAppend(offset, appendedLines, totalLines, viewportHeight int, pinnedToBottom bool) int {
	if pinnedToBottom {
		return 0
	}
	if appendedLines > 0 {
		return scrollTranscript(offset, appendedLines, totalLines, viewportHeight)
	}
	return clampTranscriptScroll(offset, totalLines, viewportHeight)
}
