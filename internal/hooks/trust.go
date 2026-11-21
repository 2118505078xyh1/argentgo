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

// ErrUntrusted 报告摘要不在信任存储中的钩子文档。
var ErrUntrusted = errors.New("hooks: document digest is not trusted")

// Digest 返回 data 的小写十六进制 SHA-256。
func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// DigestFile 对一个文件做哈希。
func DigestFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("hooks: read %s: %w", path, err)
	}
	return Digest(data), nil
}

// TrustEntry 是一组被记住的路径/摘要对。
type TrustEntry struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}

// TrustStore 记住哪些钩子文档可以运行。文档仅当其当前摘要被授予给其路径时才执行。
// 仅当其当前摘要被授予给其路径时才执行。
type TrustStore struct {
	mu      sync.Mutex
	entries map[string]string
}

// NewTrustStore 返回空存储。
func NewTrustStore() *TrustStore {
	return &TrustStore{entries: make(map[string]string)}
}

// Trust 记录 path 可以以此摘要运行。
func (s *TrustStore) Trust(path, digest string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = make(map[string]string)
	}
	s.entries[normalizePath(path)] = normalizeDigest(digest)
}

// IsTrusted 报告这个精确的路径/摘要对是否被授予。
func (s *TrustStore) IsTrusted(path, digest string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	known, ok := s.entries[normalizePath(path)]
	return ok && known == normalizeDigest(digest)
}

// TrustFile 对 path 做哈希并记录摘要；它返回该摘要。
func (s *TrustStore) TrustFile(path string) (string, error) {
	digest, err := DigestFile(path)
	if err != nil {
		return "", err
	}
	s.Trust(path, digest)
	return digest, nil
}

// Entries 按路径顺序列出被授予的对。
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

// RevokeAll 忘记所有授权。
func (s *TrustStore) RevokeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = make(map[string]string)
}

// trustDocument 是信任存储在磁盘上的形态。
type trustDocument struct {
	Version int          `json:"version"`
	Hooks   []TrustEntry `json:"hooks"`
}

// LoadFile 读取信任存储。文件缺失即为空存储。
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

// SaveFile 写入信任存储，创建父目录。
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

// Document 是字节已做哈希并被信任的钩子文档。
type Document struct {
	Path   string
	Digest string
	Config *Config
}

// LoadDocument 读取钩子文档，除非信任存储已持有其摘要否则拒绝。
// LoadDocument 读取钩子文档，除非信任存储已持有其摘要，否则拒绝它。返回的错误包装 ErrUntrusted。
func LoadDocument(path string, trust *TrustStore) (*Document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("hooks: read %s: %w", path, err)
	}
	digest := Digest(data)
	if !needsTrustGate(path, digest, trust) {
		return nil, fmt.Errorf("%s: %w", path, ErrUntrusted)
	}
	config, err := ParseConfig(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &Document{Path: path, Digest: digest, Config: config}, nil
}

// normalizePath 使信任键与分隔符无关：用 "/" 写的清单必须匹配用 "\" 拼出的路径。
// Windows 文件系统不区分大小写，因此同时折叠大小写：同一文件的不同大小写路径
// 必须落到同一个信任键，否则会被当作两份文档而要求重复授信。其他平台保留原样。
func normalizePath(path string) string {
	if path == "" {
		return ""
	}
	normalized := strings.ReplaceAll(filepath.Clean(path), "\\", "/")
	if filepath.Separator == '\\' {
		return strings.ToLower(normalized)
	}
	return normalized
}

func normalizeDigest(digest string) string {
	return strings.ToLower(strings.TrimSpace(digest))
}
