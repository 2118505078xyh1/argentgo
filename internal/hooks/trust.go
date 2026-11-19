package hooks

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// ErrUntrusted reports a hook document whose digest is not in the trust store.
var ErrUntrusted = errors.New("hooks: document digest is not trusted")

// Digest returns the lowercase hex SHA-256 of data.
func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// DigestFile hashes one file.
func DigestFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("hooks: read %s: %w", path, err)
	}
	return Digest(data), nil
}

// TrustEntry is one remembered path/digest pair.
type TrustEntry struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}

// TrustStore remembers which hook documents may run. A document is executed
// only when its current digest was granted for its path.
type TrustStore struct {
	mu      sync.Mutex
	entries map[string]string
}

// NewTrustStore returns an empty store.
func NewTrustStore() *TrustStore {
	return &TrustStore{entries: make(map[string]string)}
}

// Trust records that path may run with this digest.
func (s *TrustStore) Trust(path, digest string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = make(map[string]string)
	}
	s.entries[normalizePath(path)] = normalizeDigest(digest)
}

// IsTrusted reports whether this exact path/digest pair was granted.
func (s *TrustStore) IsTrusted(path, digest string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	known, ok := s.entries[normalizePath(path)]
	return ok && known == normalizeDigest(digest)
}

// TrustFile hashes path and records the digest; it returns the digest.
func (s *TrustStore) TrustFile(path string) (string, error) {
	digest, err := DigestFile(path)
	if err != nil {
		return "", err
	}
	s.Trust(path, digest)
	return digest, nil
}

// Entries lists the granted pairs in path order.
func (s *TrustStore) Entries() []TrustEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries := make([]TrustEntry, 0, len(s.entries))
	for path, digest := range s.entries {
		entries = append(entries, TrustEntry{Path: path, Digest: digest})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries
}

// RevokeAll forgets every grant.
func (s *TrustStore) RevokeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = make(map[string]string)
}

// trustDocument is the on-disk shape of a trust store.
type trustDocument struct {
	Version int          `json:"version"`
	Hooks   []TrustEntry `json:"hooks"`
}

// LoadFile reads a trust store. A missing file is an empty store.
func (s *TrustStore) LoadFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("hooks: read trust store %s: %w", path, err)
	}
	var document trustDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return fmt.Errorf("hooks: invalid trust store %s: %w", path, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = make(map[string]string)
	}
	for _, entry := range document.Hooks {
		if entry.Path == "" || entry.Digest == "" {
			continue
		}
		s.entries[normalizePath(entry.Path)] = normalizeDigest(entry.Digest)
	}
	return nil
}

// SaveFile writes a trust store, creating parent directories.
func (s *TrustStore) SaveFile(path string) error {
	document := trustDocument{Version: 1, Hooks: s.Entries()}
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return fmt.Errorf("hooks: encode trust store: %w", err)
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("hooks: create %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("hooks: write trust store %s: %w", path, err)
	}
	return nil
}

// Document is a hook document whose bytes were hashed and trusted.
type Document struct {
	Path   string
	Digest string
	Config *Config
}

// LoadDocument reads a hook document and refuses it unless the trust store
// already holds its digest. The returned error wraps ErrUntrusted.
func LoadDocument(path string, trust *TrustStore) (*Document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("hooks: read %s: %w", path, err)
	}
	digest := Digest(data)
	if trust == nil || !trust.IsTrusted(path, digest) {
		return nil, fmt.Errorf("%s: %w", path, ErrUntrusted)
	}
	config, err := ParseConfig(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &Document{Path: path, Digest: digest, Config: config}, nil
}

// normalizePath makes trust keys separator-insensitive: manifests written with
// "/" must match paths assembled with "\".
func normalizePath(path string) string {
	if path == "" {
		return ""
	}
	return strings.ReplaceAll(filepath.Clean(path), "\\", "/")
}

func normalizeDigest(digest string) string {
	return strings.ToLower(strings.TrimSpace(digest))
}
