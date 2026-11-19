// Package session appends run records to a JSONL file.
package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Writer appends JSON lines to one session file.
type Writer struct {
	file *os.File
}

// New creates the directory if needed and opens a timestamped session file.
func New(dir string) (*Writer, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	name := time.Now().Format("20060102-150405") + ".jsonl"
	file, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &Writer{file: file}, nil
}

// Write appends one record.
func (w *Writer) Write(kind string, payload any) error {
	record := map[string]any{
		"ts":   time.Now().UTC().Format(time.RFC3339),
		"kind": kind,
		"data": payload,
	}
	line, err := json.Marshal(record)
	if err != nil {
		return err
	}
	_, err = w.file.Write(append(line, '\n'))
	return err
}

// Close flushes and closes the file.
func (w *Writer) Close() error {
	return w.file.Close()
}
