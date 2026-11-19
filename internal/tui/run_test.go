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

	// ctrl+c with an empty input and no running turn leaves the UI.
	if _, err := writer.Write([]byte{0x03}); err != nil {
		t.Fatalf("write: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("Run did not return after ctrl+c")
	}

	written := out.String()
	if !strings.Contains(written, enterAltScreen) || !strings.Contains(written, leaveAltScreen) {
		t.Fatalf("the alternate screen was not entered and left: %q", written)
	}
	if !strings.Contains(written, "echo: hi") {
		t.Fatalf("the transcript was not handed back to the terminal: %q", written)
	}
}
