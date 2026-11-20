package tui

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

func TestRunDrivesAPromptTurnAndRestoresTheScreen(t *testing.T) {
	reader, writer := io.Pipe()
	var out bytes.Buffer
	ready := make(chan *Model, 1)
	done := make(chan error, 1)

	opts := Options{
		Root:  "/work",
		Model: "test-model",
		IO:    &IO{In: reader, Out: &out},
		Prompt: func(ctx context.Context, text string, onText func(string)) error {
			onText("echo: " + text)
			return nil
		},
		OnReady: func(m *Model) { ready <- m },
	}
	go func() { done <- Run(context.Background(), opts) }()

	var model *Model
	select {
	case model = <-ready:
	case <-time.After(5 * time.Second):
		t.Fatalf("Run never became ready")
	}

	if _, err := writer.Write([]byte("hi\r")); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitFor(t, "the streamed answer", func() bool {
		entries := model.Transcript()
		return len(entries) >= 2 && strings.Contains(entries[1].Text, "echo: hi")
	})

	// Ctrl+c leaves the UI. The streamed answer may still have a running turn
	// at this point: the first ctrl+c aborts that turn (it does not quit), and
	// a second one quits. Send them until Run returns so the test is robust to
	// the abort/end-turn race, especially under -race or heavy load.
	exited := false
	deadline := time.After(30 * time.Second)
	for !exited {
		if _, err := writer.Write([]byte{0x03}); err != nil {
			t.Fatalf("write: %v", err)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			exited = true
		case <-time.After(250 * time.Millisecond):
		case <-deadline:
			t.Fatalf("Run did not return after ctrl+c")
		}
	}

	written := out.String()
	if !strings.Contains(written, enterAltScreen) || !strings.Contains(written, leaveAltScreen) {
		t.Fatalf("the alternate screen was not entered and left: %q", written)
	}
	if !strings.Contains(written, "echo: hi") {
		t.Fatalf("the transcript was not handed back to the terminal: %q", written)
	}
}

func TestRunBracketedPasteDoesNotSubmitEarly(t *testing.T) {
	reader, writer := io.Pipe()
	var out bytes.Buffer
	ready := make(chan *Model, 1)
	prompts := make(chan string, 4)
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	opts := Options{
		Root:  "/work",
		Model: "test-model",
		IO:    &IO{In: reader, Out: &out},
		Prompt: func(ctx context.Context, text string, onText func(string)) error {
			prompts <- text
			return nil
		},
		OnReady: func(m *Model) { ready <- m },
	}
	go func() { done <- Run(ctx, opts) }()

	var model *Model
	select {
	case model = <-ready:
	case <-time.After(5 * time.Second):
		t.Fatalf("Run never became ready")
	}

	// A carriage return inside the pasted block would be decoded as Enter
	// without bracketed paste and submit only the first line.
	pasted := "first line\nsecond line\rthird line"
	if _, err := writer.Write([]byte("\x1b[200~" + pasted + "\x1b[201~")); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitFor(t, "the pasted text to land in the input", func() bool {
		return model.Input() == pasted
	})

	select {
	case got := <-prompts:
		t.Fatalf("paste submitted a prompt early: %q", got)
	case <-time.After(300 * time.Millisecond):
	}
	if entries := model.Transcript(); len(entries) != 0 {
		t.Fatalf("paste appended to the transcript: %#v", entries)
	}

	// A real Enter must still submit the whole pasted block, newlines and all.
	if _, err := writer.Write([]byte("\r")); err != nil {
		t.Fatalf("write: %v", err)
	}
	select {
	case got := <-prompts:
		if got != pasted {
			t.Fatalf("submitted = %q, want %q", got, pasted)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("Enter after paste did not submit")
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

	written := out.String()
	if !strings.Contains(written, enablePaste) || !strings.Contains(written, disablePaste) {
		t.Fatalf("bracketed paste mode was not enabled and disabled: %q", written)
	}
}
