package session

import (
	"os"
	"strings"
	"testing"
)

// A write that fails to reach the JSONL file must not leave the in-memory
// append index ahead of the disk: the minted id stays unused, the active leaf
// still points at the last record that was actually written, and the next
// append can reuse that id and continue the chain without a gap.
func TestAppendRecordRollsBackIndexOnWriteFailure(t *testing.T) {
	dir := t.TempDir()
	session := newTestSession(t, dir, "rollback")

	// Warm the cached append state with one successful append.
	if err := session.Append(KindUser, "before"); err != nil {
		t.Fatalf("warm Append: %v", err)
	}
	before, err := session.Records()
	if err != nil {
		t.Fatalf("Records before: %v", err)
	}
	lastBefore := before[len(before)-1].ID
	nextBefore := nextID(before)

	path := session.path()
	// On platforms that keep an append fd open, closing it without clearing the
	// cached stat snapshot keeps the state "valid" so the failure happens in
	// writeAppend (not in the earlier full-read rebuild). chmod alone would not
	// stop writes through a handle opened before the file became read-only.
	if keepAppendFD && session.fd != nil {
		if err := session.fd.Close(); err != nil {
			t.Fatalf("close append fd: %v", err)
		}
		session.fd = nil
	}
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatalf("chmod read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	if err := session.Append(KindAssistant, "must not be written"); err == nil {
		t.Fatal("Append to a read-only session file succeeded, want write failure")
	}

	afterFail, err := session.Records()
	if err != nil {
		t.Fatalf("Records after failure: %v", err)
	}
	if len(afterFail) != len(before) {
		t.Fatalf("records after failed append = %d, want %d", len(afterFail), len(before))
	}
	for _, record := range afterFail {
		if strings.Contains(string(record.Data), "must not be written") {
			t.Fatalf("failed append leaked into the transcript: %+v", record)
		}
	}
	for i := 1; i < len(afterFail); i++ {
		if afterFail[i].Parent != afterFail[i-1].ID {
			t.Fatalf("record %d parent = %q, want %q", i, afterFail[i].Parent, afterFail[i-1].ID)
		}
	}

	// Restore write access. The next append must succeed, reuse the id minted
	// by the failed append (no gap), and link to the same unchanged leaf.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod writable: %v", err)
	}
	recovered, err := session.AppendRecord(KindAssistant, "after recovery")
	if err != nil {
		t.Fatalf("AppendRecord after recovery: %v", err)
	}
	if recovered.ID != nextBefore {
		t.Fatalf("recovered id = %q, want %q (gap left by failed append)", recovered.ID, nextBefore)
	}
	if recovered.Parent != lastBefore {
		t.Fatalf("recovered parent = %q, want %q", recovered.Parent, lastBefore)
	}

	final, err := session.Records()
	if err != nil {
		t.Fatalf("final Records: %v", err)
	}
	if len(final) != len(before)+1 {
		t.Fatalf("final records = %d, want %d", len(final), len(before)+1)
	}
	last := final[len(final)-1]
	if last.ID != recovered.ID || last.Parent != lastBefore {
		t.Fatalf("final record = %+v, want id %q parent %q", last, recovered.ID, lastBefore)
	}
}
