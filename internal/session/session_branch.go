package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Path returns the records from the session root down to entryID, oldest first.
func (s *Session) Path(entryID string) ([]Record, error) {
	records, err := s.Records()
	if err != nil {
		return nil, err
	}
	byID := indexByID(records)
	chain := make([]Record, 0, len(records))
	seen := make(map[string]bool, len(records))
	for current := entryID; current != ""; {
		record, ok := byID[current]
		if !ok {
			return nil, fmt.Errorf("session: unknown entry %q", current)
		}
		if seen[current] {
			return nil, fmt.Errorf("session: cycle at entry %q", current)
		}
		seen[current] = true
		chain = append(chain, record)
		current = record.Parent
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain, nil
}

// Fork copies the path to entryID into a new session and returns it. The
// original session is untouched, and the copy keeps the original entry ids so
// the history it points at is unchanged.
func Fork(dir, id, entryID string) (*Session, error) {
	store, err := Open(dir)
	if err != nil {
		return nil, err
	}
	source, err := store.OpenSession(id)
	if err != nil {
		return nil, err
	}
	return source.Fork(entryID)
}

// Fork copies the path to entryID into a new session in the same directory.
func (s *Session) Fork(entryID string) (*Session, error) {
	path, err := s.Path(entryID)
	if err != nil {
		return nil, err
	}
	if len(path) == 0 {
		return nil, fmt.Errorf("session: entry %q is not on a path", entryID)
	}
	// A fork taken from a mid-turn record would otherwise lose the header and
	// its explicit title. Copy the original header when the path omits it, and
	// carry the effective title across as a fresh title record.
	all, err := s.Records()
	if err != nil {
		return nil, err
	}
	hasHeader, hasTitle := false, false
	for _, record := range path {
		if record.Kind == KindHeader {
			hasHeader = true
		}
		if record.Kind == KindTitle {
			hasTitle = true
		}
	}
	if !hasHeader {
		for _, record := range all {
			if record.Kind == KindHeader {
				path = append([]Record{record}, path...)
				break
			}
		}
	}
	if !hasTitle {
		if title := titleFromRecords(s.ID(), all); title != "" {
			data, err := encodePayload(TitleRecord{Title: title})
			if err != nil {
				return nil, err
			}
			path = append(path, Record{
				ID:     nextID(path),
				Parent: path[len(path)-1].ID,
				TS:     time.Now().UTC(),
				Kind:   KindTitle,
				Data:   data,
			})
		}
	}
	container := &Container{dir: s.dir}
	if err := os.MkdirAll(container.dir, 0o755); err != nil {
		return nil, fmt.Errorf("session: create %s: %w", container.dir, err)
	}
	id, target, err := container.newID()
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("session: create %s: %w", target, err)
	}
	writer := bufio.NewWriter(file)
	for _, record := range path {
		line, err := json.Marshal(record)
		if err != nil {
			file.Close()
			os.Remove(target)
			return nil, fmt.Errorf("session: encode fork record: %w", err)
		}
		if _, err := writer.Write(append(line, '\n')); err != nil {
			file.Close()
			os.Remove(target)
			return nil, fmt.Errorf("session: write %s: %w", target, err)
		}
	}
	if err := writer.Flush(); err != nil {
		file.Close()
		os.Remove(target)
		return nil, fmt.Errorf("session: write %s: %w", target, err)
	}
	if err := file.Close(); err != nil {
		os.Remove(target)
		return nil, fmt.Errorf("session: close %s: %w", target, err)
	}
	return &Session{dir: container.dir, id: id, active: path[len(path)-1].ID}, nil
}

// path is the session file location.
func (s *Session) path() string {
	return filepath.Join(s.dir, s.id+fileExt)
}

// activeSnapshot returns the active leaf under the mutex. Every read path that
// walks the tree goes through this helper so the s.active field is never read
// outside the lock, while the caller still does its own file I/O lock-free.
func (s *Session) activeSnapshot() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

// activePath walks from the active leaf to the root and returns the records
// oldest first. Records that are not reachable from the leaf are branches and
// stay out of the conversation.
func (s *Session) activePath(records []Record) []Record {
	byID := indexByID(records)
	leaf := s.activeSnapshot()
	if _, ok := byID[leaf]; !ok {
		if len(records) == 0 {
			return nil
		}
		leaf = records[len(records)-1].ID
	}
	chain := make([]Record, 0, len(records))
	seen := make(map[string]bool, len(records))
	for current := leaf; current != ""; {
		record, ok := byID[current]
		if !ok {
			break
		}
		if seen[current] {
			break
		}
		seen[current] = true
		chain = append(chain, record)
		current = record.Parent
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain
}

func indexByID(records []Record) map[string]Record {
	index := make(map[string]Record, len(records))
	for _, record := range records {
		index[record.ID] = record
	}
	return index
}

// nextID mints the lowest id that is not already used.
func nextID(records []Record) string {
	used := make(map[string]bool, len(records))
	for _, record := range records {
		used[record.ID] = true
	}
	for n := 1; ; n++ {
		candidate := fmt.Sprintf("n%06d", n)
		if !used[candidate] {
			return candidate
		}
	}
}
