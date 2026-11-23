package tui

import (
	"context"
	"strings"
	"testing"
	"time"
)

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestApprovalModalBlocksInputAndDenies(t *testing.T) {
	m := New(Config{})
	result := make(chan bool, 1)
	go func() {
		ok, err := m.Approve(context.Background(), "write", `{"path":"a.go"}`)
		if err != nil {
			t.Errorf("approve: %v", err)
		}
		result <- ok
	}()

	waitFor(t, "the approval modal", func() bool {
		_, _, ok := m.PendingApproval()
		return ok
	})
	name, args, ok := m.PendingApproval()
	if !ok || name != "write" || args != `{"path":"a.go"}` {
		t.Fatalf("pending = %q %q %v", name, args, ok)
	}

	// 弹窗打开时输入被阻塞。
	m.HandleKey(Key{Kind: KindRune, Rune: 'x'})
	if got := m.Input(); got != "" {
		t.Fatalf("typing into a blocked modal changed the input: %q", got)
	}

	m.HandleKey(Key{Kind: KindRune, Rune: 'n'})
	if ok := <-result; ok {
		t.Fatalf("n should deny")
	}
	if _, _, still := m.PendingApproval(); still {
		t.Fatalf("the modal should be gone after the answer")
	}
}

func TestApprovalAllowGoesThroughTheInjectedAuthority(t *testing.T) {
	m := New(Config{})
	calls := 0
	m.SetApproval(func(ctx context.Context, name, args string) (bool, error) {
		calls++
		if name != "read" || args != "a.go" {
			t.Errorf("authority called with %q %q", name, args)
		}
		return true, nil
	})

	result := make(chan bool, 1)
	go func() {
		ok, _ := m.Approve(context.Background(), "read", "a.go")
		result <- ok
	}()
	waitFor(t, "the modal", func() bool { _, _, ok := m.PendingApproval(); return ok })
	m.HandleKey(Key{Kind: KindRune, Rune: 'y'})
	if ok := <-result; !ok {
		t.Fatalf("y should allow")
	}
	if calls != 1 {
		t.Fatalf("the approval authority was called %d times, want 1", calls)
	}
}

func TestAllowForSessionSkipsTheNextModal(t *testing.T) {
	m := New(Config{})
	first := make(chan bool, 1)
	go func() {
		ok, _ := m.Approve(context.Background(), "bash", "ls")
		first <- ok
	}()
	waitFor(t, "the modal", func() bool { _, _, ok := m.PendingApproval(); return ok })
	m.HandleKey(Key{Kind: KindRune, Rune: 'a'})
	if ok := <-first; !ok {
		t.Fatalf("a should allow")
	}

	ok, err := m.Approve(context.Background(), "bash", "ls -la")
	if err != nil {
		t.Fatalf("second approve: %v", err)
	}
	if !ok {
		t.Fatalf("the second call should be allowed without a modal")
	}
	if _, _, pending := m.PendingApproval(); pending {
		t.Fatalf("no modal should have appeared")
	}
}

func TestApprovalIsCancelledByContext(t *testing.T) {
	m := New(Config{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := m.Approve(ctx, "write", "x")
		done <- err
	}()
	waitFor(t, "the modal", func() bool { _, _, ok := m.PendingApproval(); return ok })
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Approve did not return after cancellation")
	}
	waitFor(t, "the modal to clear", func() bool { _, _, ok := m.PendingApproval(); return !ok })
}

func TestStreamingAppendsToOneAssistantBlock(t *testing.T) {
	m := New(Config{})
	m.AppendUser("hi")
	m.AppendStream("hello ")
	m.AppendStream("world")
	entries := m.Transcript()
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want a user and an assistant block", len(entries))
	}
	if entries[1].Kind != EntryAssistant || entries[1].Text != "hello world" {
		t.Fatalf("assistant block = %+v", entries[1])
	}

	m.EndTurn(nil)
	m.AppendStream("again")
	entries = m.Transcript()
	if len(entries) != 3 || entries[2].Text != "again" {
		t.Fatalf("a new turn must start a new block, got %+v", entries)
	}
}

func TestEndTurnWithErrorAddsANotice(t *testing.T) {
	m := New(Config{})
	m.AppendUser("hi")
	m.EndTurn(context.Canceled)
	entries := m.Transcript()
	last := entries[len(entries)-1]
	if last.Kind != EntryNotice || last.Text != "cancelled" {
		t.Fatalf("notice = %+v, want the cancellation notice", last)
	}
	if m.Busy() {
		t.Fatalf("the model should be idle after the turn ends")
	}
}

func TestOfflineRenderNeverCallsTheTerminal(t *testing.T) {
	m := New(Config{Model: "test-model", Root: "C:/work"})
	m.AppendUser("hello")
	m.AppendStream("hi there")
	m.EndTurn(nil)
	buffer := m.Frame(80, 24)
	if buffer.Width() != 80 || buffer.Height() != 24 {
		t.Fatalf("frame is %dx%d", buffer.Width(), buffer.Height())
	}
	if !strings.Contains(buffer.RowText(23), "test-model") {
		t.Fatalf("status = %q", buffer.RowText(23))
	}
}

func TestPlainRendersTheTranscript(t *testing.T) {
	m := New(Config{})
	m.AppendUser("question")
	m.AppendStream("answer")
	m.EndTurn(nil)
	m.AppendTool("read", `{"path":"a.go"}`, "contents", false)
	plain := m.Plain()
	for _, want := range []string{"> question", "◆ answer", "● read(a.go)", "contents"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("plain transcript is missing %q:\n%s", want, plain)
		}
	}
}
