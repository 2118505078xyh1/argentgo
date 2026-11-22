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
	return s.entriesLocked()
}

// entriesLocked 返回按路径排序的被授予对。调用方必须持有 s.mu。
func (s *TrustStore) entriesLocked() []TrustEntry {
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

// SaveFile 写入信任存储，创建父目录。写入为原子操作：先在同一目录写入临时文件，
// 刷盘后再重命名到位，因此读取者只会看到完整的旧文件或完整的新文件，崩溃也不会
// 把信任库截成半截 JSON（那会让全部 hook 失信任并触发 fail-closed 停摆）。
// 整个方法持有 s.mu，使并发的 Save/Trust/Revoke/Load 互相串行，不会撕裂数据。
func (s *TrustStore) SaveFile(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	document := trustDocument{Version: 1, Hooks: s.entriesLocked()}
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return fmt.Errorf("hooks: encode trust store: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("hooks: create %s: %w", dir, err)
		}
	}
	temp, err := os.CreateTemp(dir, ".hooks-trust-*.tmp")
	if err != nil {
		return fmt.Errorf("hooks: create temporary file in %s: %w", dir, err)
	}
	name := temp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = temp.Close()
			_ = os.Remove(name)
		}
	}()

	// CreateTemp 创建的临时文件固定为 0o600，这里显式再设一次以固定最终权限，
	// 并覆盖文件系统默认权限较宽松的情况。
	if err := temp.Chmod(0o600); err != nil {
		return fmt.Errorf("hooks: restrict %s: %w", name, err)
	}
	if _, err := temp.Write(data); err != nil {
		return fmt.Errorf("hooks: write %s: %w", name, err)
	}
	// 在 rename 前把数据刷到磁盘，避免掉电后目标文件为空或被截断。
	if err := temp.Sync(); err != nil {
		return fmt.Errorf("hooks: flush %s: %w", name, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("hooks: close %s: %w", name, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("hooks: replace %s: %w", path, err)
	}
	committed = true
	// 某些文件系统在创建时忽略模式，rename 后显式再设置一次最终权限。
	_ = os.Chmod(path, 0o600)
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
