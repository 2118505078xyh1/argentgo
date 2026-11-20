package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestStartInSessionScopesJobsAndFiltersThem covers the isolation guarantee:
// jobs of two sessions sharing one registry are independent, ListForSession
// filters by session, and plain Start keeps its legacy global sequence.
func TestStartInSessionScopesJobsAndFiltersThem(t *testing.T) {
	registry := newRegistry(t)
	defer registry.waitIdle()

	a1, err := registry.StartInSession("session-A", shellCommand("echo a1"))
	if err != nil {
		t.Fatalf("start a1: %v", err)
	}
	a2, err := registry.StartInSession("session-A", shellCommand("echo a2"))
	if err != nil {
		t.Fatalf("start a2: %v", err)
	}
	b1, err := registry.StartInSession("session-B", shellCommand("echo b1"))
	if err != nil {
		t.Fatalf("start b1: %v", err)
	}
	plain, err := registry.Start(shellCommand("echo plain"))
	if err != nil {
		t.Fatalf("start plain: %v", err)
	}
	for _, job := range []Job{a1, a2, b1, plain} {
		waitForTerminal(t, registry, job.ID)
	}

	// The session is recorded on the job; a plain Start still has none.
	if a1.Session != "session-A" || a2.Session != "session-A" || b1.Session != "session-B" {
		t.Fatalf("sessions = %q, %q, %q", a1.Session, a2.Session, b1.Session)
	}
	if plain.Session != "" {
		t.Fatalf("plain session = %q, want empty", plain.Session)
	}

	// Ids are namespaced by session, never collide, and starting session jobs
	// did not consume the legacy counter: the plain job is still job1.
	seen := map[string]bool{}
	for _, job := range []Job{a1, a2, b1, plain} {
		if seen[job.ID] {
			t.Fatalf("duplicate id %q", job.ID)
		}
		seen[job.ID] = true
	}
	if plain.ID != "job1" {
		t.Fatalf("plain id = %q, want job1", plain.ID)
	}
	for _, job := range []Job{a1, a2} {
		if !strings.Contains(job.ID, "session-A") {
			t.Fatalf("id %q is not namespaced to session-A", job.ID)
		}
	}
	if !strings.Contains(b1.ID, "session-B") {
		t.Fatalf("id %q is not namespaced to session-B", b1.ID)
	}

	// Filtering returns only the requested session's jobs.
	listA, err := registry.ListForSession("session-A")
	if err != nil {
		t.Fatalf("list A: %v", err)
	}
	if len(listA) != 2 || (listA[0].ID != a1.ID && listA[1].ID != a1.ID) ||
		(listA[0].ID != a2.ID && listA[1].ID != a2.ID) {
		t.Fatalf("list A = %+v", listA)
	}
	listB, err := registry.ListForSession("session-B")
	if err != nil {
		t.Fatalf("list B: %v", err)
	}
	if len(listB) != 1 || listB[0].ID != b1.ID {
		t.Fatalf("list B = %+v", listB)
	}
	listPlain, err := registry.ListForSession("")
	if err != nil {
		t.Fatalf("list plain: %v", err)
	}
	if len(listPlain) != 1 || listPlain[0].ID != plain.ID {
		t.Fatalf("list plain = %+v", listPlain)
	}
	empty, err := registry.ListForSession("no-such-session")
	if err != nil {
		t.Fatalf("list unknown: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("list unknown = %+v", empty)
	}

	// The unchanged List still sees every job.
	all, err := registry.List()
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("list all = %d jobs, want 4", len(all))
	}
}

// TestLegacyJobsWithoutSessionStayVisible locks in jobs.json compatibility: a
// missing "session" key decodes to the empty session, so old jobs remain in the
// global list and in ListForSession(""), and cannot collide with new ids.
func TestLegacyJobsWithoutSessionStayVisible(t *testing.T) {
	dir := t.TempDir()
	// No "session" and no "sessionNext" keys: exactly what an older build wrote.
	raw := `{"next":1,"jobs":[{"id":"job1","command":"legacy","startedAt":"2020-01-01T00:00:00Z","status":"exited","exitCode":0,"logPath":"","pid":0}]}`
	if err := os.WriteFile(filepath.Join(dir, storeFile), []byte(raw), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	registry, err := New(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer registry.waitIdle()

	all, err := registry.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 1 || all[0].ID != "job1" || all[0].Session != "" {
		t.Fatalf("legacy job = %+v", all)
	}
	legacy, err := registry.ListForSession("")
	if err != nil {
		t.Fatalf("list empty session: %v", err)
	}
	if len(legacy) != 1 || legacy[0].ID != "job1" {
		t.Fatalf("legacy view = %+v", legacy)
	}
	other, err := registry.ListForSession("session-A")
	if err != nil {
		t.Fatalf("list other: %v", err)
	}
	if len(other) != 0 {
		t.Fatalf("a legacy job leaked into another session: %+v", other)
	}

	// New ids must not reuse the persisted legacy id or the loaded counter.
	plain, err := registry.Start(shellCommand("echo new-plain"))
	if err != nil {
		t.Fatalf("start plain: %v", err)
	}
	scoped, err := registry.StartInSession("session-A", shellCommand("echo new-scoped"))
	if err != nil {
		t.Fatalf("start scoped: %v", err)
	}
	waitForTerminal(t, registry, plain.ID)
	waitForTerminal(t, registry, scoped.ID)
	if plain.ID != "job2" {
		t.Fatalf("plain id = %q, want job2", plain.ID)
	}
	if plain.ID == scoped.ID || scoped.ID == "job1" {
		t.Fatalf("id collision: plain=%q scoped=%q", plain.ID, scoped.ID)
	}
	// The legacy job is still visible alongside the new ones.
	all, err = registry.List()
	if err != nil {
		t.Fatalf("list after starts: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("list after starts = %+v", all)
	}
}

// TestSessionIDsSurviveReopen verifies the session counter is persisted, so a
// restarted registry never reuses an id whose log may still be on disk.
func TestSessionIDsSurviveReopen(t *testing.T) {
	dir := t.TempDir()
	first, err := New(dir)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	before, err := first.StartInSession("session-A", shellCommand("echo before"))
	if err != nil {
		t.Fatalf("start before: %v", err)
	}
	waitForTerminal(t, first, before.ID)
	first.waitIdle()

	second, err := New(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.waitIdle()
	after, err := second.StartInSession("session-A", shellCommand("echo after"))
	if err != nil {
		t.Fatalf("start after: %v", err)
	}
	waitForTerminal(t, second, after.ID)
	if after.ID == before.ID {
		t.Fatalf("session id %q was reused after reopen", after.ID)
	}
	listed, err := second.ListForSession("session-A")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("list = %+v", listed)
	}
}

// TestConcurrentSessionStartsGetUniqueIDs checks the session counter under the
// race detector when several sessions share one registry concurrently.
func TestConcurrentSessionStartsGetUniqueIDs(t *testing.T) {
	registry := newRegistry(t)
	defer registry.waitIdle()

	const count = 4
	sessions := []string{"s1", "s2", "s3", "s4"}
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		ids  []string
		errs []error
	)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(session string) {
			defer wg.Done()
			job, err := registry.StartInSession(session, shellCommand("echo concurrent"))
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			ids = append(ids, job.ID)
		}(sessions[i])
	}
	wg.Wait()
	if len(errs) != 0 {
		t.Fatalf("start errors: %v", errs)
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
	for _, id := range ids {
		waitForTerminal(t, registry, id)
	}
}

// TestSessionIDIsSafeAsAFileName ensures odd session names cannot turn an id
// into a path that escapes the registry directory when it becomes a log name.
func TestSessionIDIsSafeAsAFileName(t *testing.T) {
	registry := newRegistry(t)
	defer registry.waitIdle()

	job, err := registry.StartInSession("../odd\\name/../x", shellCommand("echo safe"))
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitForTerminal(t, registry, job.ID)
	if strings.ContainsAny(job.ID, `/\`) {
		t.Fatalf("id %q contains a path separator", job.ID)
	}
	wantDir := registry.Dir() + string(os.PathSeparator)
	if !strings.HasPrefix(job.LogPath, wantDir) {
		t.Fatalf("log path %q escapes %q", job.LogPath, wantDir)
	}
}
