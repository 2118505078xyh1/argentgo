package session

import "testing"

// TestForkInheritsNewestTitleWhenOlderTitleIsOnPath covers a real fork bug:
// the fork point can sit after an older title record but before a newer title
// override. Checking only whether *any* title record is on the copied path
// made the fork inherit the stale older title instead of the title the source
// session actually showed when the fork was taken.
func TestForkInheritsNewestTitleWhenOlderTitleIsOnPath(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.NewSession("")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(KindUser, "first prompt"); err != nil {
		t.Fatal(err)
	}
	if err := sess.SetTitle("First"); err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(KindUser, "second prompt"); err != nil {
		t.Fatal(err)
	}
	// Freeze the fork point before the newer override lands. The copied path
	// still contains the "First" title record.
	forkPoint := sess.activeSnapshot()
	if err := sess.SetTitle("Second"); err != nil {
		t.Fatal(err)
	}
	if got := sess.Title(); got != "Second" {
		t.Fatalf("source title = %q, want %q", got, "Second")
	}

	fork, err := sess.Fork(forkPoint)
	if err != nil {
		t.Fatal(err)
	}
	if got := fork.Title(); got != "Second" {
		t.Fatalf("fork title = %q, want %q (source's effective title)", got, "Second")
	}
	// The fork's newest title record must be the carried-over title, not the
	// stale one already on the copied path.
	records, err := fork.Records()
	if err != nil {
		t.Fatal(err)
	}
	var newest string
	for _, record := range records {
		if record.Kind == KindTitle {
			newest = fieldString(record.Data, "title")
		}
	}
	if newest != "Second" {
		t.Fatalf("newest title record in fork = %q, want %q", newest, "Second")
	}
}

// TestForkCarriesDerivedTitleWhenOverrideClearedAfterForkPoint is the mirror
// image of the stale-title case: clearing an override after the fork point
// makes the source fall back to its prompt-derived title, and the fork must
// show that derived title rather than the now-cleared explicit one.
func TestForkCarriesDerivedTitleWhenOverrideClearedAfterForkPoint(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.NewSession("")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(KindUser, "the prompt"); err != nil {
		t.Fatal(err)
	}
	if err := sess.SetTitle("Named"); err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(KindUser, "later prompt"); err != nil {
		t.Fatal(err)
	}
	forkPoint := sess.activeSnapshot()
	if err := sess.SetTitle(""); err != nil {
		t.Fatal(err)
	}
	if got := sess.Title(); got != "the prompt" {
		t.Fatalf("source title = %q, want derived %q", got, "the prompt")
	}

	fork, err := sess.Fork(forkPoint)
	if err != nil {
		t.Fatal(err)
	}
	if got := fork.Title(); got != "the prompt" {
		t.Fatalf("fork title = %q, want derived %q", got, "the prompt")
	}
}

// TestForkDoesNotDuplicateTitleWhenSourceTitleUnchanged guards the common
// path: when the copied path already shows the source's effective title, no
// extra title record is appended and the fork is a faithful copy.
func TestForkDoesNotDuplicateTitleWhenSourceTitleUnchanged(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.NewSession("")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(KindUser, "first prompt"); err != nil {
		t.Fatal(err)
	}
	if err := sess.SetTitle("Only"); err != nil {
		t.Fatal(err)
	}
	forkPoint := sess.activeSnapshot()
	if err := sess.Append(KindUser, "second prompt"); err != nil {
		t.Fatal(err)
	}

	fork, err := sess.Fork(forkPoint)
	if err != nil {
		t.Fatal(err)
	}
	if got := fork.Title(); got != "Only" {
		t.Fatalf("fork title = %q, want %q", got, "Only")
	}
	records, err := fork.Records()
	if err != nil {
		t.Fatal(err)
	}
	titleRecords := 0
	for _, record := range records {
		if record.Kind == KindTitle {
			titleRecords++
		}
	}
	if titleRecords != 1 {
		t.Fatalf("fork has %d title records, want 1", titleRecords)
	}
}
