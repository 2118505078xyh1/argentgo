package session

import (
	"fmt"
	"sync"
	"testing"
)

// TestActiveConcurrentAppendAndRead exercises the s.active data race: many
// goroutines append (which writes s.active under mu) while others read through
// Records/Messages/Path (which must read s.active via activeSnapshot under mu).
//
// This machine has no cgo, so the race detector cannot run. The test therefore
// asserts logical invariants instead: no panic, the final record count is
// exact, and every id/parent link is intact. Running with -race on a cgo host
// would additionally catch the original unsynchronised read.
func TestActiveConcurrentAppendAndRead(t *testing.T) {
	const (
		writers   = 8
		perWriter = 20
		readers   = 6
	)

	s := newTestSession(t, t.TempDir(), "concurrent")

	// assertChain checks a snapshot a concurrent reader may observe. Because
	// appends are serialised by mu and only ever extend a single chain, any
	// snapshot the reader sees is a clean prefix: ids are unique and every
	// non-root parent resolves inside the snapshot.
	assertChain := func(records []Record) {
		ids := make(map[string]bool, len(records))
		for _, r := range records {
			if ids[r.ID] {
				t.Errorf("duplicate id %q in concurrent read", r.ID)
			}
			ids[r.ID] = true
		}
		for _, r := range records {
			if r.Parent != "" && !ids[r.Parent] {
				t.Errorf("record %q parent %q missing from concurrent read", r.ID, r.Parent)
			}
		}
	}

	stop := make(chan struct{})
	var readersWG sync.WaitGroup
	for r := 0; r < readers; r++ {
		readersWG.Add(1)
		go func() {
			defer readersWG.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				records, err := s.Records()
				if err != nil {
					t.Errorf("concurrent Records: %v", err)
					return
				}
				records = withoutSchemaHeader(records)
				assertChain(records)
				if len(records) > 0 {
					leaf := records[len(records)-1].ID
					path, err := s.Path(leaf)
					if err != nil {
						t.Errorf("concurrent Path(%q): %v", leaf, err)
						return
					}
					if len(path) != len(records) {
						t.Errorf("concurrent Path returned %d records, snapshot has %d", len(path), len(records))
					}
				}
				// Messages walks the tree via activePath -> activeSnapshot.
				for _, entry := range s.Messages() {
					if entry.Role == "" {
						t.Error("concurrent Messages returned an entry with no role")
						break
					}
				}
			}
		}()
	}

	var writersWG sync.WaitGroup
	for w := 0; w < writers; w++ {
		writersWG.Add(1)
		go func(w int) {
			defer writersWG.Done()
			for i := 0; i < perWriter; i++ {
				if _, err := s.AppendRecord(KindMessage, map[string]string{
					"role":    "user",
					"content": fmt.Sprintf("w%d-%d", w, i),
				}); err != nil {
					t.Errorf("concurrent AppendRecord: %v", err)
					return
				}
			}
		}(w)
	}
	writersWG.Wait()
	close(stop)
	readersWG.Wait()

	// Authoritative check once all writers are done: exact count and a single
	// unbroken id chain from the header to the active leaf.
	records, err := s.Records()
	if err != nil {
		t.Fatalf("final Records: %v", err)
	}
	records = withoutSchemaHeader(records)
	want := 1 + writers*perWriter // header + appended records
	if len(records) != want {
		t.Fatalf("final records = %d, want %d", len(records), want)
	}
	assertChain(records)
	if records[0].Parent != "" {
		t.Errorf("root parent = %q, want empty", records[0].Parent)
	}
	for i := 1; i < len(records); i++ {
		if records[i].Parent != records[i-1].ID {
			t.Fatalf("record %d parent = %q, want %q (id chain broken)", i, records[i].Parent, records[i-1].ID)
		}
	}

	path, err := s.Path(records[len(records)-1].ID)
	if err != nil {
		t.Fatalf("final Path: %v", err)
	}
	if len(path) != len(records) {
		t.Fatalf("final Path length = %d, want %d", len(path), len(records))
	}
	for i := range path {
		if path[i].ID != records[i].ID {
			t.Fatalf("final Path record %d id = %q, want %q", i, path[i].ID, records[i].ID)
		}
	}

	if msgs := s.Messages(); len(msgs) != writers*perWriter {
		t.Fatalf("final Messages = %d, want %d", len(msgs), writers*perWriter)
	}
}
