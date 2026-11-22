package auth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// readFileDoc 读取磁盘上的原始文档，绕过解密以断言存储格式。
func readFileDoc(t *testing.T, store *Store) fileDoc {
	t.Helper()
	raw, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatalf("read %s: %v", store.Path(), err)
	}
	var doc fileDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", store.Path(), err)
	}
	return doc
}

// writeFileDoc 直接写入给定文档，用于模拟迁移前的旧明文 auth.json。
func writeFileDoc(t *testing.T, store *Store, doc fileDoc) {
	t.Helper()
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("marshal legacy doc: %v", err)
	}
	if err := os.WriteFile(store.Path(), raw, storeFileMode); err != nil {
		t.Fatalf("write legacy doc: %v", err)
	}
}

func findStored(stored []fileToken, provider string) (fileToken, bool) {
	for _, entry := range stored {
		if entry.Provider == provider {
			return entry, true
		}
	}
	return fileToken{}, false
}

// TestStoreEncryptsTokenOnWrite 断言 Windows 下令牌以 dpapi:v1: 前缀的 base64 密文写盘，
// 其他平台保持明文现状。
func TestStoreEncryptsTokenOnWrite(t *testing.T) {
	store := scratchStore(t)
	if err := store.Put(Token{Provider: "openai", Kind: KindAPIKey, Access: "secret-value"}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	doc := readFileDoc(t, store)
	entry, ok := findStored(doc.Tokens, "openai")
	if !ok {
		t.Fatal("token missing on disk")
	}
	if runtime.GOOS == "windows" {
		if !strings.HasPrefix(entry.Access, tokenSealPrefix) {
			t.Fatalf("stored access = %q, want prefix %q", entry.Access, tokenSealPrefix)
		}
		encoded := strings.TrimPrefix(entry.Access, tokenSealPrefix)
		blob, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			t.Fatalf("stored access is not base64: %v", err)
		}
		if len(blob) == 0 {
			t.Fatal("stored ciphertext is empty")
		}
		if string(blob) == "secret-value" {
			t.Fatal("stored access is plaintext, want DPAPI ciphertext")
		}
		return
	}
	if entry.Access != "secret-value" {
		t.Fatalf("non-Windows stored access = %q, want plaintext", entry.Access)
	}
}

// TestStoreReadsLegacyPlaintextToken 证明迁移期无前缀的旧明文令牌照常读出。
func TestStoreReadsLegacyPlaintextToken(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "creds")
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	expires := time.Unix(1_800_000_000, 0).UTC()
	writeFileDoc(t, store, fileDoc{
		Version: storeVersion,
		Tokens: []fileToken{{
			Provider:  "openai",
			Kind:      KindOAuth,
			Access:    "legacy-access",
			Refresh:   "legacy-refresh",
			ExpiresAt: expires.Unix(),
		}},
	})
	got, ok := store.Get("openai")
	if !ok {
		t.Fatal("legacy token missing")
	}
	if got.Access != "legacy-access" || got.Refresh != "legacy-refresh" {
		t.Fatalf("legacy token = %+v, want plaintext secrets", got)
	}
	if !got.ExpiresAt.Equal(expires) {
		t.Fatalf("expiry = %v, want %v", got.ExpiresAt, expires)
	}
	// 明文不应被误判为密文（解封路径应保持原值）。
	plain, err := tokenDecrypt("legacy-access")
	if err != nil || plain != "legacy-access" {
		t.Fatalf("decryptToken(plaintext) = %q, %v", plain, err)
	}
}

// TestStoreUpgradesLegacyTokenOnWrite 证明读到的旧明文令牌在写回时升级为加密格式。
func TestStoreUpgradesLegacyTokenOnWrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "creds")
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	writeFileDoc(t, store, fileDoc{
		Version: storeVersion,
		Tokens: []fileToken{{
			Provider: "openai", Kind: KindAPIKey, Access: "legacy-key",
		}},
	})
	// 触发一次与旧令牌无关的写盘；save() 会重加密全部令牌。
	if err := store.Put(Token{Provider: "zai", Kind: KindAPIKey, Access: "new-key"}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	entry, ok := findStored(readFileDoc(t, store).Tokens, "openai")
	if !ok {
		t.Fatal("legacy token lost after rewrite")
	}
	if runtime.GOOS == "windows" {
		if !strings.HasPrefix(entry.Access, tokenSealPrefix) {
			t.Fatalf("legacy token not upgraded: %q", entry.Access)
		}
	} else if entry.Access != "legacy-key" {
		t.Fatalf("non-Windows legacy token = %q, want unchanged plaintext", entry.Access)
	}
	// 升级后仍应能正确读回。
	got, ok := store.Get("openai")
	if !ok || got.Access != "legacy-key" {
		t.Fatalf("upgraded token read back = %+v, %v", got, ok)
	}
}

// TestSealTokenFallsBackOnEncryptError 断言 DPAPI 加密失败时，未加密的新令牌
// 仍可回退明文并发出告警（历史行为）。
func TestSealTokenFallsBackOnEncryptError(t *testing.T) {
	var warns []string
	warn := func(format string, args ...any) {
		warns = append(warns, fmt.Sprintf(format, args...))
	}
	fail := func(string) (string, error) { return "", errors.New("dpapi unavailable") }

	got, err := sealToken("secret", false, fail, warn)
	if err != nil || got != "secret" {
		t.Fatalf("sealToken = %q, %v; want plaintext fallback", got, err)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "回退明文") {
		t.Fatalf("warnings = %v, want one fallback warning", warns)
	}
	// 空秘密不应触发加密或告警。
	warns = nil
	got, err = sealToken("", false, fail, warn)
	if err != nil || got != "" {
		t.Fatalf("sealToken(empty) = %q, %v, want empty", got, err)
	}
	if len(warns) != 0 {
		t.Fatalf("empty secret produced warnings: %v", warns)
	}
}

// TestSealTokenRefusesPlaintextDowngrade 锁定 fail-closed：此前已加密的令牌
// 在加密失败时必须拒绝写回，绝不降级为明文（#22）。
func TestSealTokenRefusesPlaintextDowngrade(t *testing.T) {
	var warns []string
	warn := func(format string, args ...any) {
		warns = append(warns, fmt.Sprintf(format, args...))
	}
	fail := func(string) (string, error) { return "", errors.New("dpapi unavailable") }

	got, err := sealToken("secret", true, fail, warn)
	if !errors.Is(err, ErrTokenEncryptDowngrade) || got != "" {
		t.Fatalf("sealToken(wasSealed) = %q, %v; want downgrade refusal", got, err)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "拒绝明文降级") {
		t.Fatalf("warnings = %v, want downgrade warning", warns)
	}

	// 显式允许降级时恢复历史行为。
	old := allowPlaintextTokenFallback
	allowPlaintextTokenFallback = true
	defer func() { allowPlaintextTokenFallback = old }()
	got, err = sealToken("secret", true, fail, warn)
	if err != nil || got != "secret" {
		t.Fatalf("explicit fallback = %q, %v; want plaintext", got, err)
	}
}

// TestStoreRefusesToDowngradeOnWrite 端到端锁定 fail-closed：磁盘上已有加密条目时，
// 后续 Put 遇到加密失败必须整笔拒绝，文件字节不变（#22）。
func TestStoreRefusesToDowngradeOnWrite(t *testing.T) {
	store := scratchStore(t)
	// 先写入一条"已加密"的条目（用可注入的加密器打上 dpapi:v1: 前缀）。
	origEncrypt := tokenEncrypt
	tokenEncrypt = func(s string) (string, error) { return tokenSealPrefix + "cipher", nil }
	if err := store.Put(Token{Provider: "openai", Kind: KindAPIKey, Access: "first"}); err != nil {
		tokenEncrypt = origEncrypt
		t.Fatal(err)
	}
	before, err := os.ReadFile(store.Path())
	if err != nil {
		tokenEncrypt = origEncrypt
		t.Fatal(err)
	}
	doc := readFileDoc(t, store)
	if entry, _ := findStored(doc.Tokens, "openai"); !strings.HasPrefix(entry.Access, tokenSealPrefix) {
		tokenEncrypt = origEncrypt
		t.Fatalf("setup: entry not sealed: %q", entry.Access)
	}

	// 加密器失效后再次写同一 provider：必须拒绝，且磁盘不变。
	tokenEncrypt = func(string) (string, error) { return "", errors.New("dpapi unavailable") }
	defer func() { tokenEncrypt = origEncrypt }()
	err = store.Put(Token{Provider: "openai", Kind: KindAPIKey, Access: "second"})
	if !errors.Is(err, ErrTokenEncryptDowngrade) {
		t.Fatalf("Put = %v, want ErrTokenEncryptDowngrade", err)
	}
	after, _ := os.ReadFile(store.Path())
	if string(after) != string(before) {
		t.Fatal("auth.json changed despite refused downgrade")
	}
}

// TestUnsealTokenDropsSecretOnDecryptError 断言密文无法解密时不会把密文当 bearer 返回。
func TestUnsealTokenDropsSecretOnDecryptError(t *testing.T) {
	var warns []string
	warn := func(format string, args ...any) {
		warns = append(warns, fmt.Sprintf(format, args...))
	}
	fail := func(string) (string, error) { return "", errors.New("dpapi unprotect failed") }

	if got := unsealToken(tokenSealPrefix+"garbage", fail, warn); got != "" {
		t.Fatalf("unsealToken = %q, want empty on decrypt failure", got)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "解密失败") {
		t.Fatalf("warnings = %v, want one decrypt warning", warns)
	}
}

// TestTokenCryptRoundTripWindows 对真实 DPAPI 做加密/解密往返（仅 Windows 运行）。
func TestTokenCryptRoundTripWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("DPAPI 仅在 Windows 可用")
	}
	const secret = "sk-敏感值-with-unicode-✓"
	sealed, err := encryptToken(secret)
	if err != nil {
		t.Fatalf("encryptToken: %v", err)
	}
	if !strings.HasPrefix(sealed, tokenSealPrefix) {
		t.Fatalf("sealed = %q, want prefix %q", sealed, tokenSealPrefix)
	}
	if strings.Contains(sealed, secret) {
		t.Fatal("ciphertext contains plaintext secret")
	}
	opened, err := decryptToken(sealed)
	if err != nil {
		t.Fatalf("decryptToken: %v", err)
	}
	if opened != secret {
		t.Fatalf("round trip = %q, want %q", opened, secret)
	}
	// 损坏的 base64 必须报错而不能静默返回密文。
	if _, err := decryptToken(tokenSealPrefix + "!!!not-base64!!!"); err == nil {
		t.Fatal("decryptToken accepted malformed base64")
	}
}
