package session

import (
	"crypto/rand"
	"encoding/hex"
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
	session := &Session{
		dir: c.dir,
		id:  strings.TrimSuffix(filepath.Base(path), fileExt),
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

// List reports every session, newest first.
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
		records, err := readRecords(filepath.Join(c.dir, entry.Name()))
		if err != nil {
			continue
		}
		meta := metaFromRecords(id, records)
		if meta.Updated.IsZero() {
			if info, err := entry.Info(); err == nil {
				meta.Updated = info.ModTime()
			}
		}
		metas = append(metas, meta)
	}
	sort.Slice(metas, func(i, j int) bool {
		if metas[i].Updated.Equal(metas[j].Updated) {
			return metas[i].ID > metas[j].ID
		}
		return metas[i].Updated.After(metas[j].Updated)
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
