package auth

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func scratchStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "nested", "creds"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return store
}

func TestStoreRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "creds")
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	token := Token{
		Provider:  "openai",
		Kind:      KindOAuth,
		Access:    "access-1",
		Refresh:   "refresh-1",
		ExpiresAt: time.Unix(1_800_000_000, 0).UTC(),
		Account:   "user@example.com",
	}
	if err := store.Put(token); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := store.Put(Token{Provider: "zai", Kind: KindAPIKey, Access: "key-1"}); err != nil {
		t.Fatalf("Put api key: %v", err)
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, ok := reopened.Get("openai")
	if !ok {
		t.Fatal("openai token missing after reload")
	}
	if got.Access != "access-1" || got.Refresh != "refresh-1" || got.Account != "user@example.com" {
		t.Fatalf("round trip lost fields: %+v", got)
	}
	if !got.ExpiresAt.Equal(token.ExpiresAt) {
		t.Fatalf("expiry = %v, want %v", got.ExpiresAt, token.ExpiresAt)
	}
	if key, ok := reopened.Get("zai"); !ok || key.Bearer() != "key-1" || !key.ExpiresAt.IsZero() {
		t.Fatalf("api key round trip = %+v, %v", key, ok)
	}

	// Replacement keeps a single entry per provider.
	if err := reopened.Put(Token{Provider: "openai", Kind: KindOAuth, Access: "access-2"}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	list, err := reopened.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("List returned %d tokens, want 2: %+v", len(list), list)
	}
	if list[0].Provider != "openai" || list[1].Provider != "zai" {
		t.Fatalf("List is not sorted by provider: %+v", list)
	}

	if err := reopened.Delete("openai"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := reopened.Get("openai"); ok {
		t.Fatal("deleted token still present")
	}
	// Deleting a missing provider is not an error.
	if err := reopened.Delete("openai"); err != nil {
		t.Fatalf("second Delete: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, entry := range entries {
		if entry.Name() != storeFileName {
			t.Fatalf("unexpected file left behind: %s", entry.Name())
		}
	}
}

func TestStoreRejectsInvalidTokens(t *testing.T) {
	store := scratchStore(t)
	cases := []struct {
		name  string
		token Token
	}{
		{"no provider", Token{Kind: KindOAuth, Access: "a"}},
		{"no kind", Token{Provider: "x", Access: "a"}},
		{"unknown kind", Token{Provider: "x", Kind: "cookie", Access: "a"}},
		{"no secret", Token{Provider: "x", Kind: KindOAuth}},
	}
	for _, test := range cases {
		if err := store.Put(test.token); err == nil {
			t.Fatalf("%s: Put succeeded, want error", test.name)
		}
	}
}

func TestStorePermissionsAreOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no POSIX file modes")
	}
	store := scratchStore(t)
	if err := store.Put(Token{Provider: "zai", Kind: KindAPIKey, Access: "secret"}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	info, err := os.Stat(store.Path())
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("mode = %o, want 600", mode)
	}
}

func TestStoreStatus(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	store := scratchStore(t)
	if err := store.Put(Token{Provider: "api", Kind: KindAPIKey, Access: "key"}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	cases := []struct {
		name     string
		provider string
		expires  time.Time
		want     Status
	}{
		{"missing", "nothing", time.Time{}, StatusMissing},
		{"no expiry", "api", time.Time{}, StatusValid},
		{"far from expiry", "oauth", now.Add(time.Hour), StatusValid},
		{"inside the skew", "oauth", now.Add(4 * time.Minute), StatusExpiring},
		{"exactly the skew", "oauth", now.Add(expirySkew), StatusExpiring},
		{"just over the skew", "oauth", now.Add(expirySkew + time.Second), StatusValid},
		{"expired", "oauth", now.Add(-time.Second), StatusExpired},
	}
	for _, test := range cases {
		if test.provider == "oauth" {
			if err := store.Put(Token{
				Provider:  "oauth",
				Kind:      KindOAuth,
				Access:    "token",
				ExpiresAt: test.expires,
			}); err != nil {
				t.Fatalf("%s: Put: %v", test.name, err)
			}
		}
		if got := store.Status(test.provider, now); got != test.want {
			t.Fatalf("%s: Status = %q, want %q", test.name, got, test.want)
		}
	}
}

func TestOpenRejectsAnEmptyDirectory(t *testing.T) {
	if _, err := Open("  "); err == nil {
		t.Fatal("Open accepted an empty directory")
	}
}
