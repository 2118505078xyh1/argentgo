package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
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

	// tokenSealPrefix 标记该令牌值已被平台加密层封装，便于识别与迁移。
	// 无此前缀的值视为旧明文，读取时照常使用，写回时自动升级为加密格式。
	tokenSealPrefix = "dpapi:v1:"
)

// tokenEncrypt/tokenDecrypt 由平台特定的 token_crypt_*.go 提供：Windows 下用 DPAPI
// 封装，其他平台为明文直通。用变量间接调用便于测试注入降级（加密失败）场景。
var (
	tokenEncrypt = encryptToken
	tokenDecrypt = decryptToken

	// warnf 输出加密降级等警告；默认写标准错误日志，测试中可替换。
	warnf = func(format string, args ...any) { log.Printf(format, args...) }
)

// sealToken 在写盘前加密令牌；加密失败（如 DPAPI 在非交互会话不可用）时回退明文
// 并告警，避免让整个登录流程因为加密问题而失败。
func sealToken(plain string, encrypt func(string) (string, error), warn func(string, ...any)) string {
	if plain == "" {
		return ""
	}
	sealed, err := encrypt(plain)
	if err != nil {
		warn("auth: 令牌加密失败，已回退明文存储: %v", err)
		return plain
	}
	return sealed
}

// unsealToken 在读盘后解密令牌；解密失败时空出该秘密，避免把密文误当作 bearer 发给 provider。
// 无 tokenSealPrefix 前缀的历史明文由 decryptToken 原样返回，实现向后兼容。
func unsealToken(stored string, decrypt func(string) (string, error), warn func(string, ...any)) string {
	if stored == "" {
		return ""
	}
	plain, err := decrypt(stored)
	if err != nil {
		warn("auth: 令牌解密失败，已忽略该秘密: %v", err)
		return ""
	}
	return plain
}

// fileToken 是磁盘形态：Unix 时间戳使零值不出现在文件中，而非序列化第 1 年。
// 而非序列化第 1 年。
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

// Store 把 provider 令牌保存在某目录下的单个 JSON 文件中。
type Store struct {
	dir  string
	path string
	mu   sync.Mutex
}

// Open 准备 dir（以模式 0700 创建它）并返回由其中 auth.json 文件支撑的 store。直到第一次查找才读取任何内容。
// 由其中 auth.json 文件支撑的 store。直到第一次查找才读取任何内容。
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

// DefaultDir 返回每用户凭据目录 <config>/argent。
func DefaultDir() (string, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("auth: locate the config directory: %w", err)
	}
	return filepath.Join(root, "argent"), nil
}

// Path 是支撑 store 的 JSON 文件。
func (s *Store) Path() string { return s.path }

// Get 返回为 provider 存储的令牌。
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

// List 返回所有存储的令牌，按 provider id 排序。
func (s *Store) List() ([]Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

// Put 存储令牌，替换同一 provider 的任何旧条目。
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

// Delete 删除 provider 的令牌并报告它是否存在。
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

// Status 报告 provider 的令牌是有效、接近过期、已过期还是不存在。now 是显式的，使调用方和测试共享一个
// 或不存在。now 是显式的，使调用方和测试共享一个
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
			Access:   unsealToken(entry.Access, tokenDecrypt, warnf),
			Refresh:  unsealToken(entry.Refresh, tokenDecrypt, warnf),
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

// save 通过临时文件写入整个文档，使崩溃绝不会留下半写的凭据文件。
// 留下半写的凭据文件。
func (s *Store) save(tokens []Token) error {
	doc := fileDoc{Version: storeVersion, Tokens: make([]fileToken, 0, len(tokens))}
	for _, token := range tokens {
		entry := fileToken{
			Provider: token.Provider,
			Kind:     token.Kind,
			Access:   sealToken(token.Access, tokenEncrypt, warnf),
			Refresh:  sealToken(token.Refresh, tokenEncrypt, warnf),
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
	// Rename 保留临时文件的模式；对创建时忽略模式的.filesystem 再次设置它。
	// 在创建时忽略模式的 filesystem 再次设置它。
	_ = os.Chmod(s.path, storeFileMode)
	return nil
}
