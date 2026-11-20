package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Meta is what a session list shows without loading a transcript into memory.
type Meta struct {
	ID      string    `json:"id"`
	Title   string    `json:"title"`
	Updated time.Time `json:"updated"`
	Turns   int       `json:"turns"`
	// Modified is the transcript file's mtime, captured during List and used
	// only to order sessions with equal in-record timestamps. It is never
	// persisted (the sidecar still describes size+mtime elsewhere), so adding
	// it does not change the on-disk format.
	Modified time.Time `json:"-"`
}

// Container is a directory that holds session files.
type Container struct {
	dir string
}

// Store is the entry point for one session directory.
type Store struct {
	dir string
}

// Open creates dir when it is missing and returns a store over it.
func Open(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("session: empty directory")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("session: create %s: %w", dir, err)
	}
	return &Store{dir: dir}, nil
}

// Dir reports the directory that holds the session files.
func (s *Store) Dir() string { return s.dir }

// Container returns the directory handle behind the store.
func (s *Store) Container() *Container { return &Container{dir: s.dir} }

// NewSession creates a session file holding one header record.
func (s *Store) NewSession(title string) (*Session, error) {
	return s.Container().NewSession(title)
}

// OpenSession reopens a stored session by id (an exact id or a unique prefix).
func (s *Store) OpenSession(id string) (*Session, error) {
	return s.Container().OpenSession(id)
}

// List reports every session, newest first.
func (s *Store) List() ([]Meta, error) { return s.Container().List() }

// Find reports the sessions whose id or title contains query.
func (s *Store) Find(query string) ([]Meta, error) { return s.Container().Find(query) }

// Delete removes one session file.
func (s *Store) Delete(id string) error { return s.Container().Delete(id) }

// OpenSession reopens a stored session by id (an exact id or a unique prefix).
func OpenSession(dir, id string) (*Session, error) {
	return (&Container{dir: dir}).OpenSession(id)
}

// List reports every session under dir, newest first.
func List(dir string) ([]Meta, error) { return (&Container{dir: dir}).List() }

// Find reports the sessions under dir whose id or title contains query.
func Find(dir, query string) ([]Meta, error) { return (&Container{dir: dir}).Find(query) }

// Delete removes one session file under dir.
func Delete(dir, id string) error { return (&Container{dir: dir}).Delete(id) }

// Dir reports the directory that holds the session files.
func (c *Container) Dir() string { return c.dir }

// NewSession creates a session file holding one header record.
func (c *Container) NewSession(title string) (*Session, error) {
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return nil, fmt.Errorf("session: create %s: %w", c.dir, err)
	}
	id, _, err := c.newID()
	if err != nil {
		return nil, err
	}
	session := &Session{dir: c.dir, id: id}
	header := Header{Title: strings.TrimSpace(title), Created: time.Now().UTC()}
	if err := session.Append(KindHeader, header); err != nil {
		return nil, err
	}
	return session, nil
}

// OpenSession reopens a stored session by id (an exact id or a unique prefix).
func (c *Container) OpenSession(id string) (*Session, error) {
	path, err := c.resolvePath(id)
	if err != nil {
		return nil, err
	}
	if _, err := ValidateAndRepair(path, true); err != nil {
		return nil, fmt.Errorf("resume session %q: %w", id, err)
	}
	session := &Session{
		dir: c.dir,
		id:  transcriptStem(filepath.Base(path)),
	}
	records, err := readRecords(path)
	if err != nil {
		return nil, err
	}
	if len(records) > 0 {
		session.active = records[len(records)-1].ID
	}
	return session, nil
}

// List reports every session, newest first. Transcripts are not parsed when a
// matching sidecar exists; a missing, corrupt, or stale one falls back to the
// original full scan and is refreshed on the way out.
func (c *Container) List() ([]Meta, error) {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("session: read %s: %w", c.dir, err)
	}
	metas := make([]Meta, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), fileExt) {
			continue
		}
		id := entry.Name()[:len(entry.Name())-len(fileExt)]
		path := filepath.Join(c.dir, entry.Name())
		// Listing never repairs: hard-corrupt transcripts are invisible here,
		// and a torn tail stays untouched until an explicit resume.
		if _, err := ValidateAndRepair(path, false); err != nil {
			continue
		}

		info, statErr := entry.Info()
		var meta Meta
		var cached bool
		if statErr == nil {
			meta, cached = loadIndex(path, info, id)
		}
		if !cached {
			records, err := readRecords(path)
			if err != nil {
				continue
			}
			meta = metaFromRecords(id, records)
			if statErr == nil {
				saveIndex(path, info, meta)
			}
		}
		if meta.Updated.IsZero() && statErr == nil {
			meta.Updated = info.ModTime()
		}
		// Sidecars do not store Modified; capture it on every List so a cache
		// hit still carries the information needed to break Updated ties.
		if statErr == nil {
			meta.Modified = info.ModTime()
		}
		metas = append(metas, meta)
	}
	sort.Slice(metas, func(i, j int) bool {
		if !metas[i].Updated.Equal(metas[j].Updated) {
			return metas[i].Updated.After(metas[j].Updated)
		}
		// The in-record clock can stamp two sessions identically (e.g. two
		// sessions created in the same second). The file's mtime records the
		// real order of writes, so a later append moves a session to the top;
		// fall back to id only when both timestamps are identical.
		if !metas[i].Modified.Equal(metas[j].Modified) {
			return metas[i].Modified.After(metas[j].Modified)
		}
		return metas[i].ID > metas[j].ID
	})
	return metas, nil
}

// Find reports the sessions whose id or title contains query, newest first.
func (c *Container) Find(query string) ([]Meta, error) {
	all, err := c.List()
	if err != nil {
		return nil, err
	}
	needle := strings.ToLower(strings.TrimSpace(query))
	if needle == "" {
		return all, nil
	}
	matches := make([]Meta, 0, len(all))
	for _, meta := range all {
		if strings.Contains(strings.ToLower(meta.ID), needle) ||
			strings.Contains(strings.ToLower(meta.Title), needle) {
			matches = append(matches, meta)
		}
	}
	return matches, nil
}

// Delete removes one session file.
func (c *Container) Delete(id string) error {
	path, err := c.resolvePath(id)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("session: delete %s: %w", path, err)
	}
	// The sidecar is only a cache; remove it with its transcript so a later
	// session with a reused id cannot inherit stale metadata.
	_ = os.Remove(path + idxExt)
	return nil
}

// path turns an id into a file path, rejecting anything that could escape dir.
func (c *Container) path(id string) (string, error) {
	clean := strings.TrimSpace(id)
	if clean == "" {
		return "", fmt.Errorf("session: empty session id")
	}
	if strings.HasSuffix(strings.ToLower(clean), fileExt) {
		clean = clean[:len(clean)-len(fileExt)]
	}
	if clean == "" || clean == "." || clean == ".." ||
		strings.ContainsAny(clean, `/\:*?"<>|`) {
		return "", fmt.Errorf("session: invalid session id %q", id)
	}
	return filepath.Join(c.dir, clean+fileExt), nil
}

// resolvePath accepts an exact id, otherwise a unique case-insensitive prefix.
func (c *Container) resolvePath(id string) (string, error) {
	exact, err := c.path(id)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(exact); err == nil {
		return exact, nil
	}
	want := strings.ToLower(strings.TrimSuffix(filepath.Base(exact), fileExt))
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return "", fmt.Errorf("session: read %s: %w", c.dir, err)
	}
	var matches []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), fileExt) {
			continue
		}
		stem := entry.Name()[:len(entry.Name())-len(fileExt)]
		if strings.HasPrefix(strings.ToLower(stem), want) {
			matches = append(matches, filepath.Join(c.dir, entry.Name()))
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("session: no session with id %q", id)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("session: ambiguous session id %q (%d matches)", id, len(matches))
	}
}

// transcriptStem strips a trailing transcript extension from a file name
// regardless of case. Discovery paths match the ".jsonl" suffix
// case-insensitively (see List and resolvePath), so the id derived from a
// resolved path must strip it the same way. A case-sensitive strings.TrimSuffix
// would leave "SESSION.JSONL" as the id, making Session.path() point at
// "SESSION.JSONL.jsonl" and every read come back empty on case-sensitive
// filesystems. Names without the suffix are returned unchanged.
func transcriptStem(name string) string {
	if len(name) >= len(fileExt) &&
		strings.EqualFold(name[len(name)-len(fileExt):], fileExt) {
		return name[:len(name)-len(fileExt)]
	}
	return name
}

// newID mints a timestamped id with a random suffix and its unused file path.
func (c *Container) newID() (string, string, error) {
	stamp := time.Now().Format("20060102-150405")
	for attempt := 0; attempt < 32; attempt++ {
		id := stamp + "-" + randomSuffix()
		path := filepath.Join(c.dir, id+fileExt)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return id, path, nil
		}
	}
	return "", "", fmt.Errorf("session: could not allocate a session id in %s", c.dir)
}

func randomSuffix() string {
	buf := make([]byte, 2)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%04x", uint16(time.Now().UnixNano()))
	}
	return hex.EncodeToString(buf)
}

// Sidecars are caches, never the authority for session content. An entry
// stores only the fields List/Search need plus the transcript's size and mtime,
// so an append that bypasses the cache, or any external edit, is detected and
// the transcript is parsed again.
const (
	idxExt       = ".idx"
	indexVersion = 1
)

type indexEntry struct {
	Version int   `json:"version"`
	Size    int64 `json:"size"`
	MTime   int64 `json:"mtime"`
	Meta    Meta  `json:"meta"`
}

// loadIndex returns the cached meta only when the sidecar is readable, current
// for this version, and describes the transcript's exact size and mtime. Any
// doubt is reported as a miss and handled by a full scan.
func loadIndex(path string, info os.FileInfo, id string) (Meta, bool) {
	data, err := os.ReadFile(path + idxExt)
	if err != nil {
		return Meta{}, false
	}
	var entry indexEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return Meta{}, false
	}
	if entry.Version != indexVersion || entry.Size != info.Size() ||
		entry.MTime != info.ModTime().UnixNano() || entry.Meta.ID != id {
		return Meta{}, false
	}
	return entry.Meta, true
}

// saveIndex refreshes a sidecar atomically so a concurrent reader never sees a
// half-written file. Failure is harmless: the next read just parses the file.
func saveIndex(path string, info os.FileInfo, meta Meta) {
	data, err := json.Marshal(indexEntry{
		Version: indexVersion,
		Size:    info.Size(),
		MTime:   info.ModTime().UnixNano(),
		Meta:    meta,
	})
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".idx-*.tmp")
	if err != nil {
		return
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return
	}
	if err := os.Rename(name, path+idxExt); err != nil {
		os.Remove(name)
	}
}

// metaFromRecords derives a list entry from the transcript alone.
func metaFromRecords(id string, records []Record) Meta {
	meta := Meta{ID: id, Title: titleFromRecords(id, records)}
	for _, record := range records {
		if !record.TS.IsZero() {
			meta.Updated = record.TS
		}
		switch record.Kind {
		case KindAssistant:
			meta.Turns++
		case KindMessage:
			if fieldString(record.Data, "role") == "assistant" {
				meta.Turns++
			}
		}
	}
	return meta
}
