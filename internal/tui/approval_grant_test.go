package tui

import (
	"context"
	"testing"
	"time"
)

func TestSessionGrantIsScopedToTheApprovedCommand(t *testing.T) {
	m := New(Config{})
	first := make(chan bool, 1)
	go func() {
		ok, err := m.Approve(context.Background(), "bash", `{"command":"ls"}`)
		if err != nil {
			t.Errorf("first approve: %v", err)
		}
		first <- ok
	}()
	waitFor(t, "the first modal", func() bool { _, _, ok := m.PendingApproval(); return ok })
	m.HandleKey(Key{Kind: KindRune, Rune: 'a'})
	if ok := <-first; !ok {
		t.Fatal("a should allow the first command")
	}

	second := make(chan bool, 1)
	go func() {
		ok, err := m.Approve(context.Background(), "bash", `{"command":"sudo rm -rf /"}`)
		if err != nil {
			t.Errorf("second approve: %v", err)
		}
		second <- ok
	}()
	waitFor(t, "a modal for the different command", func() bool { _, _, ok := m.PendingApproval(); return ok })
	name, args, _ := m.PendingApproval()
	if name != "bash" || args != `{"command":"sudo rm -rf /"}` {
		t.Fatalf("unexpected pending request: %q %q", name, args)
	}
	m.HandleKey(Key{Kind: KindRune, Rune: 'n'})
	if ok := <-second; ok {
		t.Fatal("a different command must not be granted by the session allow")
	}
}

func TestCoarseNameGrantFromOlderCallersIsStillHonoured(t *testing.T) {
	m := New(Config{})
	m.allowed["bash"] = true
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	ok, err := m.Approve(ctx, "bash", `{"command":"sudo"}`)
	if err != nil || !ok {
		t.Fatalf("a pre-existing coarse grant should skip the modal, got ok=%v err=%v", ok, err)
	}
	if _, _, pending := m.PendingApproval(); pending {
		t.Fatal("a coarse grant must not raise a modal")
	}
}
