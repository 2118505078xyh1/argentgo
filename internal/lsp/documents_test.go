package lsp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

// publishPayload builds a textDocument/publishDiagnostics body with a single
// diagnostic carrying message. A nil version emulates a server that omits the
// optional version field.
func publishPayload(uri string, version *int, message string) []byte {
	params := map[string]any{
		"uri":         uri,
		"diagnostics": []any{},
	}
	if message != "" {
		params["diagnostics"] = []any{map[string]any{
			"range": map[string]any{
				"start": map[string]any{"line": 0, "character": 0},
				"end":   map[string]any{"line": 0, "character": 1},
			},
			"severity": 1,
			"source":   "fake",
			"message":  message,
		}}
	}
	if version != nil {
		params["version"] = *version
	}
	payload, err := json.Marshal(params)
	if err != nil {
		panic(err)
	}
	return payload
}

func intPtr(v int) *int { return &v }

// diagClient returns just enough client state to exercise storeDiagnostics
// without starting a transport.
func diagClient(uri string, version int) *Client {
	return &Client{
		docs:      map[string]*documentState{uri: {version: version}},
		published: map[string]publication{},
		waiters:   map[uint64]diagWaiter{},
	}
}

func TestStoreDiagnosticsIgnoresStaleVersion(t *testing.T) {
	const uri = "file:///stale.go"

	t.Run("newer then stale keeps the current publication", func(t *testing.T) {
		c := diagClient(uri, 2)
		c.storeDiagnostics(publishPayload(uri, intPtr(2), "v2"))

		if c.seq != 1 {
			t.Fatalf("seq after v2 = %d, want 1", c.seq)
		}
		if entry := c.published[uri]; len(entry.items) != 1 || entry.items[0].Message != "v2" {
			t.Fatalf("published after v2 = %+v, want v2", entry)
		}

		ch, cancel := c.addWaiter(uri)
		defer cancel()
		c.storeDiagnostics(publishPayload(uri, intPtr(1), "v1"))

		if c.seq != 1 {
			t.Fatalf("seq after stale v1 = %d, want 1 (unchanged)", c.seq)
		}
		if entry := c.published[uri]; len(entry.items) != 1 || entry.items[0].Message != "v2" {
			t.Fatalf("published after stale v1 = %+v, want unchanged v2", entry)
		}
		select {
		case <-ch:
			t.Fatal("a stale publish woke a waiter")
		default:
		}
		if len(c.waiters) != 1 {
			t.Fatalf("waiters after stale publish = %d, want 1", len(c.waiters))
		}
	})

	t.Run("stale with no prior publication stays empty", func(t *testing.T) {
		c := diagClient(uri, 2)
		ch, cancel := c.addWaiter(uri)
		defer cancel()
		c.storeDiagnostics(publishPayload(uri, intPtr(1), "v1"))

		if c.seq != 0 {
			t.Fatalf("seq after stale-only publish = %d, want 0", c.seq)
		}
		if _, ok := c.published[uri]; ok {
			t.Fatal("a stale publish created a cache entry")
		}
		select {
		case <-ch:
			t.Fatal("a stale publish woke a waiter")
		default:
		}
		if len(c.waiters) != 1 {
			t.Fatalf("waiters after stale publish = %d, want 1", len(c.waiters))
		}
	})

	t.Run("current version is accepted and wakes waiters", func(t *testing.T) {
		c := diagClient(uri, 2)
		ch, cancel := c.addWaiter(uri)
		defer cancel()
		c.storeDiagnostics(publishPayload(uri, intPtr(2), "v2"))

		if c.seq != 1 {
			t.Fatalf("seq = %d, want 1", c.seq)
		}
		if entry := c.published[uri]; len(entry.items) != 1 || entry.items[0].Message != "v2" {
			t.Fatalf("published = %+v, want v2", entry)
		}
		select {
		case <-ch:
		default:
			t.Fatal("a current publish did not wake the waiter")
		}
		if len(c.waiters) != 0 {
			t.Fatalf("waiters after current publish = %d, want 0", len(c.waiters))
		}
	})

	t.Run("unversioned publish keeps the original behaviour", func(t *testing.T) {
		c := diagClient(uri, 2)
		c.storeDiagnostics(publishPayload(uri, nil, "unversioned"))

		if c.seq != 1 {
			t.Fatalf("seq = %d, want 1", c.seq)
		}
		if entry := c.published[uri]; len(entry.items) != 1 || entry.items[0].Message != "unversioned" {
			t.Fatalf("published = %+v, want unversioned", entry)
		}
	})
}

func TestDiagnosticsIgnoresStalePublishAfterChange(t *testing.T) {
	client, _ := startPipeClient(t, fakeOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	path := filepath.Join(t.TempDir(), "main.go")
	writeTestFile(t, path, "package main\n")
	uri, err := client.Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := client.Diagnostics(ctx, uri, 3*time.Second); err != nil {
		t.Fatalf("Diagnostics after Open: %v", err)
	}

	if err := client.DidChange(ctx, uri, "package main\n\nfunc main() {}\n"); err != nil {
		t.Fatalf("DidChange: %v", err)
	}
	diagnostics, err := client.Diagnostics(ctx, uri, 3*time.Second)
	if err != nil {
		t.Fatalf("Diagnostics after change: %v", err)
	}
	if len(diagnostics) != 1 || diagnostics[0].Message != "changed" {
		t.Fatalf("diagnostics after change = %+v, want 'changed'", diagnostics)
	}

	// A delayed publish for the previous version lands after the change.
	client.storeDiagnostics(publishPayload(uri, intPtr(1), "stale"))

	// The caller's mark still points at the change; the current result must win.
	diagnostics, err = client.Diagnostics(context.Background(), uri, 0)
	if err != nil {
		t.Fatalf("Diagnostics after stale publish: %v", err)
	}
	if len(diagnostics) != 1 || diagnostics[0].Message != "changed" {
		t.Fatalf("diagnostics after stale publish = %+v, want current 'changed'", diagnostics)
	}
}
