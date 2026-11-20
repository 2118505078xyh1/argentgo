package agent

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestInstructionSnapshotAllLayerOrdering checks that All presents layers in
// user -> thread -> project order regardless of the argument order used to
// build the snapshot.
func TestInstructionSnapshotAllLayerOrdering(t *testing.T) {
	s := BuildSnapshot(
		[]string{"user-a", "user-b"},     // user
		[]string{"project-a"},            // project
		[]string{"thread-a", "thread-b"}, // thread
	)
	want := []string{"user-a", "user-b", "thread-a", "thread-b", "project-a"}
	got := s.All()
	if len(got) != len(want) {
		t.Fatalf("All() length = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("All()[%d] = %q, want %q (got %v)", i, got[i], want[i], got)
		}
	}
}

// TestInstructionSnapshotCloneIsDeepCopy checks that Clone allocates new slices
// so mutations to the copy never reach the source (and vice versa).
func TestInstructionSnapshotCloneIsDeepCopy(t *testing.T) {
	src := BuildSnapshot([]string{"u"}, []string{"p"}, []string{"t"})
	clone := src.Clone()

	// Mutate the clone's slices and backing arrays.
	clone.User[0] = "changed-user"
	clone.Thread[0] = "changed-thread"
	clone.Project[0] = "changed-project"
	clone.User = append(clone.User, "extra")

	if src.User[0] != "u" || src.Thread[0] != "t" || src.Project[0] != "p" || len(src.User) != 1 {
		t.Fatalf("source changed after mutating clone: %+v", src)
	}

	// The reverse direction must hold too.
	src.User[0] = "src-mutated"
	if clone.User[0] != "changed-user" {
		t.Fatalf("clone changed after mutating source: %+v", clone)
	}
}

// TestInstructionSnapshotFingerprintStableAndContentSensitive checks that the
// fingerprint is deterministic, changes when content changes, and also changes
// when identical content moves to a different layer.
func TestInstructionSnapshotFingerprintStableAndContentSensitive(t *testing.T) {
	a := BuildSnapshot([]string{"u"}, []string{"p"}, []string{"t"})
	b := BuildSnapshot([]string{"u"}, []string{"p"}, []string{"t"})
	if a.Fingerprint() != b.Fingerprint() {
		t.Fatalf("fingerprint not stable: %q != %q", a.Fingerprint(), b.Fingerprint())
	}

	changed := BuildSnapshot([]string{"u"}, []string{"p"}, []string{"t!"})
	if a.Fingerprint() == changed.Fingerprint() {
		t.Fatalf("fingerprint did not change with content: %q", a.Fingerprint())
	}

	moved := BuildSnapshot([]string{"t"}, []string{"p"}, []string{"u"})
	if a.Fingerprint() == moved.Fingerprint() {
		t.Fatalf("fingerprint ignored layer placement: %q", a.Fingerprint())
	}
}

// TestBuildSnapshotBoundsTotalBytes checks that the combined layers never
// exceed the 32 KiB budget, even with many oversized entries.
func TestBuildSnapshotBoundsTotalBytes(t *testing.T) {
	big := strings.Repeat("x", 9000)
	s := BuildSnapshot(
		[]string{big, big, big},
		[]string{big, big},
		[]string{big, big, big, big},
	)
	if s.Bytes > MaxSnapshotBytes {
		t.Fatalf("Bytes = %d, want <= %d", s.Bytes, MaxSnapshotBytes)
	}
	sum := 0
	for _, v := range s.All() {
		sum += len(v)
	}
	if sum != s.Bytes {
		t.Fatalf("sum of entry lengths = %d, want Bytes = %d", sum, s.Bytes)
	}
}

// TestBuildSnapshotCJKClipRuneSafe checks that a multi-byte (CJK) entry is cut
// on a rune boundary and stays valid UTF-8 when it exceeds the budget.
func TestBuildSnapshotCJKClipRuneSafe(t *testing.T) {
	cjk := strings.Repeat("界", 20000) // 60000 bytes > 32 KiB budget
	s := BuildSnapshot([]string{cjk}, nil, nil)
	if len(s.User) != 1 {
		t.Fatalf("expected one clipped entry, got %d", len(s.User))
	}
	entry := s.User[0]
	if len(entry) > MaxSnapshotBytes {
		t.Fatalf("clipped entry length = %d, want <= %d", len(entry), MaxSnapshotBytes)
	}
	if !utf8.ValidString(entry) {
		t.Fatalf("clipped CJK entry is not valid UTF-8 (len %d)", len(entry))
	}
	// A 3-byte rune cut must leave a multiple-of-3 byte count.
	if len(entry)%3 != 0 {
		t.Fatalf("clipped CJK entry length = %d, want a multiple of 3", len(entry))
	}
	if s.Bytes != len(entry) {
		t.Fatalf("Bytes = %d, want clipped length %d", s.Bytes, len(entry))
	}
}

// TestBuildSnapshotScrubsControlCharacters checks the minimal scrub removes
// NUL/control bytes and trims whitespace while keeping ordinary text.
func TestBuildSnapshotScrubsControlCharacters(t *testing.T) {
	s := BuildSnapshot([]string{"  keep\x00this\x01\nand\tthis  "}, nil, nil)
	if len(s.User) != 1 {
		t.Fatalf("expected one entry, got %d", len(s.User))
	}
	want := "keepthis\nand\tthis"
	if s.User[0] != want {
		t.Fatalf("scrubbed entry = %q, want %q", s.User[0], want)
	}
}

// TestInstructionSnapshotInheritAddsNoteAndDeepCopy checks that Inherit appends
// the inheritance note to Thread, returns an independent copy, and stays bounded.
func TestInstructionSnapshotInheritAddsNoteAndDeepCopy(t *testing.T) {
	parent := BuildSnapshot([]string{"u"}, []string{"p"}, []string{"t1", "t2"})
	child := parent.Inherit()

	got := child.Thread[len(child.Thread)-1]
	if got != InheritanceNote {
		t.Fatalf("last thread entry = %q, want %q", got, InheritanceNote)
	}
	if len(child.Thread) != len(parent.Thread)+1 {
		t.Fatalf("thread length = %d, want %d", len(child.Thread), len(parent.Thread)+1)
	}
	if child.User[0] != "u" || child.Thread[0] != "t1" || child.Project[0] != "p" {
		t.Fatalf("inherited content lost: %+v", child)
	}
	if child.Bytes > MaxSnapshotBytes {
		t.Fatalf("inherited Bytes = %d, want <= %d", child.Bytes, MaxSnapshotBytes)
	}

	// Source must be untouched and independent of the child.
	child.Thread[0] = "mutated"
	if parent.Thread[0] != "t1" {
		t.Fatalf("parent changed after mutating inherited snapshot: %+v", parent)
	}
}

// TestInstructionSnapshotInheritKeepsNoteAtBudget checks that the inheritance
// marker survives even when the parent snapshot already fills the byte budget.
func TestInstructionSnapshotInheritKeepsNoteAtBudget(t *testing.T) {
	full := BuildSnapshot([]string{strings.Repeat("x", MaxSnapshotBytes)}, nil, nil)
	child := full.Inherit()
	if child.Thread[len(child.Thread)-1] != InheritanceNote {
		t.Fatalf("inheritance note missing at budget: %+v", child.Thread)
	}
	if child.Bytes > MaxSnapshotBytes {
		t.Fatalf("inherited Bytes = %d, want <= %d", child.Bytes, MaxSnapshotBytes)
	}
}

// TestMergeSnapshotsDedupAndOrdering checks parent-first ordering, de-duplication
// by content, and that Bytes is recomputed from the merged result.
func TestMergeSnapshotsDedupAndOrdering(t *testing.T) {
	parent := BuildSnapshot([]string{"u1", "shared"}, []string{"p1"}, []string{"t1"})
	additional := BuildSnapshot([]string{"shared", "u2"}, []string{"p2", "p1"}, []string{"t2", "t1"})

	merged := MergeSnapshots(parent, additional)
	wantUser := []string{"u1", "shared", "u2"}
	if len(merged.User) != len(wantUser) {
		t.Fatalf("merged user = %v, want %v", merged.User, wantUser)
	}
	for i := range wantUser {
		if merged.User[i] != wantUser[i] {
			t.Fatalf("merged user = %v, want %v", merged.User, wantUser)
		}
	}
	if strings.Join(merged.Project, ",") != "p1,p2" {
		t.Fatalf("merged project = %v, want [p1 p2]", merged.Project)
	}
	if strings.Join(merged.Thread, ",") != "t1,t2" {
		t.Fatalf("merged thread = %v, want [t1 t2]", merged.Thread)
	}

	sum := 0
	for _, v := range merged.All() {
		sum += len(v)
	}
	if merged.Bytes != sum {
		t.Fatalf("merged Bytes = %d, want %d", merged.Bytes, sum)
	}
}

// TestInstructionSnapshotEmpty checks the zero-value snapshot behaves sanely:
// empty All, a stable fingerprint, empty clone, an inheritable note, and an
// empty merge.
func TestInstructionSnapshotEmpty(t *testing.T) {
	var empty InstructionSnapshot
	if len(empty.All()) != 0 {
		t.Fatalf("empty All() = %v, want no entries", empty.All())
	}
	if empty.Bytes != 0 {
		t.Fatalf("empty Bytes = %d, want 0", empty.Bytes)
	}
	if empty.Fingerprint() != (InstructionSnapshot{}).Fingerprint() {
		t.Fatal("empty fingerprint is not stable")
	}
	if len(empty.Clone().All()) != 0 {
		t.Fatal("empty clone should stay empty")
	}

	inherited := empty.Inherit()
	if len(inherited.Thread) != 1 || inherited.Thread[0] != InheritanceNote {
		t.Fatalf("inherited empty snapshot = %+v, want just the note", inherited.Thread)
	}

	merged := MergeSnapshots(empty, empty)
	if len(merged.All()) != 0 || merged.Bytes != 0 {
		t.Fatalf("merged empty snapshots = %+v, want empty", merged)
	}
}
