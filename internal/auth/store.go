package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	storeFileName = "auth.json"
	storeVersion  = 1
	storeFileMode = 0o600
	storeDirMode  = 0o700
)

// fileToken is the on-disk shape: a Unix timestamp keeps zero values out of the
// file instead of serialising year 1.
type fileToken struct {
	Provider  string `json:"provider"`
	Kind      string `json:"kind"`
	Access    string `json:"access,omitempty"`
	Refresh   string `json:"refresh,omitempty"`
	ExpiresAt int64  `json:"expires_at,omitempty"`
	Account   string `json:"account,omitempty"`
}

type fileDoc struct {
	Version int         `json:"version"`
	Tokens  []fileToken `json:"tokens"`
}

// Store keeps provider tokens in one JSON file inside a directory.
type Store struct {
	dir  string
	path string
	mu   sync.Mutex
}

// Open prepares dir (creating it with mode 0700) and returns a store backed by
// the auth.json file inside it. Nothing is read until the first lookup.
func Open(dir string) (*Store, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, errors.New("auth: store directory must not be empty")
	}
	if err := os.MkdirAll(dir, storeDirMode); err != nil {
		return nil, fmt.Errorf("auth: create %s: %w", dir, err)
	}
	return &Store{dir: dir, path: filepath.Join(dir, storeFileName)}, nil
}

// DefaultDir returns the per-user credential directory, <config>/argent.
func DefaultDir() (string, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("auth: locate the config directory: %w", err)
	}
	return filepath.Join(root, "argent"), nil
}

// Path is the JSON file backing the store.
func (s *Store) Path() string { return s.path }

// Get returns the token stored for provider.
func (s *Store) Get(provider string) (Token, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tokens, err := s.load()
	if err != nil {
		return Token{}, false
	}
	for _, token := range tokens {
		if token.Provider == provider {
			return token, true
		}
	}
	return Token{}, false
}

// List returns every stored token, ordered by provider id.
func (s *Store) List() ([]Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

// Put stores a token, replacing any previous entry for the same provider.
func (s *Store) Put(token Token) error {
	if strings.TrimSpace(token.Provider) == "" {
		return errors.New("auth: token provider must not be empty")
	}
	if token.Kind == "" {
		return errors.New("auth: token kind must not be empty")
	}
	if token.Kind != KindOAuth && token.Kind != KindAPIKey {
		return fmt.Errorf("auth: unknown token kind %q", token.Kind)
	}
	if token.Bearer() == "" {
		return errors.New("auth: token has no secret")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tokens, err := s.load()
	if err != nil {
		return err
	}
	replaced := false
	for index := range tokens {
		if tokens[index].Provider == token.Provider {
			tokens[index] = token
			replaced = true
			break
		}
	}
	if !replaced {
		tokens = append(tokens, token)
	}
	return s.save(tokens)
}

// Delete removes the token for provider and reports whether it existed.
func (s *Store) Delete(provider string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tokens, err := s.load()
	if err != nil {
		return err
	}
	kept := tokens[:0]
	found := false
	for _, token := range tokens {
		if token.Provider == provider {
			found = true
			continue
		}
		kept = append(kept, token)
	}
	if !found {
		return nil
	}
	return s.save(kept)
}

// Status reports whether the provider's token is valid, close to expiry,
// already expired, or absent. now is explicit so callers and tests share one
// clock.
func (s *Store) Status(provider string, now time.Time) Status {
	token, ok := s.Get(provider)
	if !ok || token.Bearer() == "" {
		return StatusMissing
	}
	if token.ExpiresAt.IsZero() {
		return StatusValid
	}
	switch left := token.ExpiresAt.Sub(now); {
	case left <= 0:
		return StatusExpired
	case left <= expirySkew:
		return StatusExpiring
	default:
		return StatusValid
	}
}

func (s *Store) load() ([]Token, error) {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("auth: read %s: %w", s.path, err)
	}
	var doc fileDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("auth: parse %s: %w", s.path, err)
	}
	tokens := make([]Token, 0, len(doc.Tokens))
	for _, entry := range doc.Tokens {
		if strings.TrimSpace(entry.Provider) == "" {
			continue
		}
		token := Token{
			Provider: entry.Provider,
			Kind:     entry.Kind,
			Access:   entry.Access,
			Refresh:  entry.Refresh,
			Account:  entry.Account,
		}
		if entry.ExpiresAt > 0 {
			token.ExpiresAt = time.Unix(entry.ExpiresAt, 0).UTC()
		}
		tokens = append(tokens, token)
	}
	sort.Slice(tokens, func(i, j int) bool { return tokens[i].Provider < tokens[j].Provider })
	return tokens, nil
}

// save writes the whole document through a temporary file so a crash never
// leaves a half-written credential file behind.
func (s *Store) save(tokens []Token) error {
	doc := fileDoc{Version: storeVersion, Tokens: make([]fileToken, 0, len(tokens))}
	for _, token := range tokens {
		entry := fileToken{
			Provider: token.Provider,
			Kind:     token.Kind,
			Access:   token.Access,
			Refresh:  token.Refresh,
			Account:  token.Account,
		}
		if !token.ExpiresAt.IsZero() {
			entry.ExpiresAt = token.ExpiresAt.Unix()
		}
		doc.Tokens = append(doc.Tokens, entry)
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("auth: encode credentials: %w", err)
	}
	data = append(data, '\n')

	temp, err := os.CreateTemp(s.dir, ".auth-*.tmp")
	if err != nil {
		return fmt.Errorf("auth: create a temporary file in %s: %w", s.dir, err)
	}
	name := temp.Name()
	defer os.Remove(name)
	if err := temp.Chmod(storeFileMode); err != nil {
		temp.Close()
		return fmt.Errorf("auth: restrict %s: %w", name, err)
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("auth: write %s: %w", name, err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("auth: flush %s: %w", name, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("auth: close %s: %w", name, err)
	}
	if err := os.Rename(name, s.path); err != nil {
		return fmt.Errorf("auth: replace %s: %w", s.path, err)
	}
	// Rename keeps the temp file's mode; set it again for filesystems that
	// ignore modes on create.
	_ = os.Chmod(s.path, storeFileMode)
	return nil
}
