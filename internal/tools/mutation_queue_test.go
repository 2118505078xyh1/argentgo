package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFileMutationKeyUsesNearestExistingAncestor(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sub", "missing", "file.txt")
	key := fileMutationKey(path)

	// Equivalent spellings (including dot/dot-dot segments) must collapse to
	// the same key even though the file itself does not exist yet.
	alt := filepath.Join(root, "sub", ".", "missing", "..", "missing", "file.txt")
	if other := fileMutationKey(alt); other != key {
		t.Fatalf("equivalent paths produced different keys: %q vs %q", key, other)
	}
	// Windows path comparison is case-insensitive.
	if other := fileMutationKey(strings.ToUpper(path)); other != key {
		t.Fatalf("case variant produced a different key: %q vs %q", key, other)
	}
	if other := fileMutationKey(filepath.Join(root, "sub", "missing", "other.txt")); other == key {
		t.Fatalf("different files share the key %q", key)
	}
}

func TestFileMutationQueueSerializesSamePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.txt")

	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan struct{})
	go withFileMutation(path, func() {
		close(firstEntered)
		<-releaseFirst
		close(firstDone)
	})
	<-firstEntered // The first writer now holds the lock.

	secondEntered := make(chan struct{})
	secondDone := make(chan struct{})
	go func() {
		withFileMutation(path, func() { close(secondEntered) })
		close(secondDone)
	}()

	select {
	case <-secondEntered:
		t.Error("second writer entered the critical section while the first held the lock")
	case <-time.After(2 * time.Second):
		// Expected: the second writer waits for the first one to finish.
		// The timer is only a deadlock guard, not a performance assertion.
	}

	close(releaseFirst)
	<-firstDone
	<-secondEntered
	<-secondDone
}

func TestFileMutationQueueDifferentPathsDoNotBlock(t *testing.T) {
	root := t.TempDir()
	pathA := filepath.Join(root, "a.txt")
	pathB := filepath.Join(root, "b.txt")

	enteredA := make(chan struct{})
	releaseA := make(chan struct{})
	go withFileMutation(pathA, func() {
		close(enteredA)
		<-releaseA
	})
	<-enteredA // A is still holding its lock.

	enteredB := make(chan struct{})
	releaseB := make(chan struct{})
	go withFileMutation(pathB, func() {
		close(enteredB)
		<-releaseB
	})

	select {
	case <-enteredB:
		// B reached its critical section while A was still inside its own:
		// a channel handshake, not a wall-clock speed measurement.
	case <-time.After(2 * time.Second):
		close(releaseA)
		t.Fatal("writes to different paths blocked each other")
	}
	close(releaseA)
	close(releaseB)
}

func TestWriteToolConcurrentWritesAreNotInterleaved(t *testing.T) {
	root := t.TempDir()
	registry := New(root, true)
	args := func(content string) string {
		raw, err := json.Marshal(struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}{"shared.txt", content})
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	first := strings.Repeat("A", 1<<16)
	second := strings.Repeat("B", 1<<16)

	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, content := range []string{first, second} {
		wg.Add(1)
		go func(raw string) {
			defer wg.Done()
			<-start
			if _, err := registry.Run(context.Background(), "write", raw); err != nil {
				t.Errorf("write: %v", err)
			}
		}(args(content))
	}
	close(start)
	wg.Wait()

	data, err := os.ReadFile(filepath.Join(root, "shared.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != first && got != second {
		t.Fatalf("final content is a torn mix of two concurrent writes (len=%d)", len(got))
	}
}

func TestEditToolConcurrentEditsBothSurvive(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "shared.txt")
	if err := os.WriteFile(path, []byte("alpha beta"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := New(root, true)

	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, pair := range [][2]string{{"alpha", "ALPHA"}, {"beta", "BETA"}} {
		wg.Add(1)
		go func(old, newText string) {
			defer wg.Done()
			raw, err := json.Marshal(struct {
				Path string `json:"path"`
				Old  string `json:"old"`
				New  string `json:"new"`
			}{"shared.txt", old, newText})
			if err != nil {
				t.Errorf("marshal: %v", err)
				return
			}
			<-start
			if _, err := registry.Run(context.Background(), "edit", string(raw)); err != nil {
				t.Errorf("edit: %v", err)
			}
		}(pair[0], pair[1])
	}
	close(start)
	wg.Wait()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "ALPHA BETA" {
		t.Fatalf("content = %q, want %q (one edit was lost)", got, "ALPHA BETA")
	}
}
