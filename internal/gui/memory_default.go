package gui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// NewFileMemory returns the stdlib default Memory backend. Entries live in
// <storage>/memory/<scope>__<key>.md as timestamped markdown bullets. An empty
// storageDir means ~/.argent.
func NewFileMemory(storageDir string) Memory {
	return &fileMemory{root: storageRoot(storageDir)}
}

type fileMemory struct {
	root string
}

func (m *fileMemory) dir() string {
	return filepath.Join(m.root, "memory")
}

// memoryPath maps a scope/key pair onto one markdown file. Keys are sanitized
// so a UI-supplied key can never escape the memory directory.
func (m *fileMemory) memoryPath(scope, key string) string {
	return filepath.Join(m.dir(), sanitizeMemoryPart(scope)+"__"+sanitizeMemoryPart(key)+".md")
}

func sanitizeMemoryPart(value string) string {
	value = strings.TrimSpace(value)
	var out strings.Builder
	for _, ch := range value {
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9', ch == '.', ch == '-', ch == '_':
			out.WriteRune(ch)
		default:
			out.WriteByte('_')
		}
	}
	text := out.String()
	if text == "" {
		return "_"
	}
	return text
}

// Add appends one "- <RFC3339> <key>: <value>" bullet. Repeating a key grows
// the file instead of overwriting earlier facts.
func (m *fileMemory) Add(scope, key, value string) error {
	if strings.TrimSpace(scope) == "" || strings.TrimSpace(key) == "" {
		return fmt.Errorf("memory: scope and key are required")
	}
	if err := os.MkdirAll(m.dir(), 0o755); err != nil {
		return fmt.Errorf("memory: create %s: %w", m.dir(), err)
	}
	path := m.memoryPath(scope, key)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("memory: open %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	line := fmt.Sprintf("- %s %s: %s\n", time.Now().UTC().Format(time.RFC3339), strings.TrimSpace(key), value)
	if _, err := file.WriteString(line); err != nil {
		return fmt.Errorf("memory: write %s: %w", path, err)
	}
	return nil
}

// Forget deletes the file for one scope/key. Removing a missing entry is a
// no-op so the page can call it twice without surfacing an error.
func (m *fileMemory) Forget(scope, key string) error {
	if strings.TrimSpace(scope) == "" || strings.TrimSpace(key) == "" {
		return fmt.Errorf("memory: scope and key are required")
	}
	err := os.Remove(m.memoryPath(scope, key))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("memory: remove: %w", err)
	}
	return nil
}

func (m *fileMemory) List() ([]MemoryRow, error) {
	rows, err := m.listRaw()
	if err != nil {
		return nil, err
	}
	return redactMemoryRows(rows), nil
}

func (m *fileMemory) Search(query string) ([]MemoryRow, error) {
	rows, err := m.listRaw()
	if err != nil {
		return nil, err
	}
	needle := strings.ToLower(strings.TrimSpace(query))
	if needle == "" {
		return redactMemoryRows(rows), nil
	}
	matched := make([]MemoryRow, 0, len(rows))
	for _, row := range rows {
		if strings.Contains(strings.ToLower(row.Key), needle) ||
			strings.Contains(strings.ToLower(row.Value), needle) {
			matched = append(matched, row)
		}
	}
	return redactMemoryRows(matched), nil
}

// listRaw enumerates <storage>/memory/*.md and parses each bullet. The scope
// comes from the filename; the original key and value come from the bullet.
func (m *fileMemory) listRaw() ([]MemoryRow, error) {
	entries, err := os.ReadDir(m.dir())
	if err != nil {
		if os.IsNotExist(err) {
			return []MemoryRow{}, nil
		}
		return nil, fmt.Errorf("memory: read %s: %w", m.dir(), err)
	}
	rows := make([]MemoryRow, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".md") {
			continue
		}
		scope, _, ok := strings.Cut(entry.Name(), "__")
		if !ok {
			continue
		}
		data, err := os.ReadFile(filepath.Join(m.dir(), entry.Name()))
		if err != nil {
			continue
		}
		source := "memory/" + entry.Name()
		for _, line := range strings.Split(string(data), "\n") {
			key, value, ok := parseMemoryBullet(line)
			if !ok {
				continue
			}
			rows = append(rows, MemoryRow{Scope: scope, Key: key, Value: value, Source: &source})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Scope != rows[j].Scope {
			return rows[i].Scope < rows[j].Scope
		}
		if rows[i].Key != rows[j].Key {
			return rows[i].Key < rows[j].Key
		}
		return rows[i].Value < rows[j].Value
	})
	return rows, nil
}

func parseMemoryBullet(line string) (key, value string, ok bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), "- ")
	if !ok {
		return "", "", false
	}
	_, rest, ok = strings.Cut(rest, " ") // drop the timestamp
	if !ok {
		return "", "", false
	}
	key, value, ok = strings.Cut(rest, ": ")
	return strings.TrimSpace(key), value, ok
}

func redactMemoryRows(rows []MemoryRow) []MemoryRow {
	out := make([]MemoryRow, len(rows))
	copy(out, rows)
	for i := range out {
		out[i].Value = redactSecrets(out[i].Value)
	}
	return out
}
