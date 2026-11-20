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

	// Ctrl+c 退出 UI。此时流式回答可能仍有运行中的回合
	// 在这里：第一个 ctrl+c 中止该回合（而不是退出），
	// 第二个才退出。持续发送直到 Run 返回，使测试对
	// 中止/结束回合的竞态稳健，尤其是在 -race 或重负载下。
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

	// 没有括号粘贴时，粘贴块中的回车会被解码为 Enter
	// 并只提交第一行。
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

	// 真正的 Enter 仍必须提交整个粘贴块，包括所有换行。
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
