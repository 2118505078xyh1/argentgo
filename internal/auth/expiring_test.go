package auth

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// expiringFixture 构建一个覆盖扫描器必须区分的所有分类的 store。now 固定，使每个令牌的状态明确。
// 扫描器必须区分。now 固定，使每个令牌状态明确。
func expiringFixture(t *testing.T, now time.Time) *Store {
	t.Helper()
	store := scratchStore(t)
	tokens := []Token{
		{Provider: "github-copilot", Kind: KindOAuth, Access: "access-gh", Refresh: "refresh-gh", ExpiresAt: now.Add(-time.Hour)},
		{Provider: "openai", Kind: KindOAuth, Access: "access-oa", Refresh: "refresh-oa", ExpiresAt: now.Add(10 * time.Minute)},
		{Provider: "zai", Kind: KindOAuth, Access: "access-zai", Refresh: "refresh-zai", ExpiresAt: now.Add(2 * time.Hour)},
		{Provider: "通义千问", Kind: KindOAuth, Access: "access-cjk", Refresh: "refresh-cjk", ExpiresAt: now.Add(2 * time.Minute)},
		{Provider: "anthropic", Kind: KindAPIKey, Access: "sk-anthropic-key"}, // no deadline: never expires
	}
	for _, token := range tokens {
		if err := store.Put(token); err != nil {
			t.Fatalf("Put(%s): %v", token.Provider, err)
		}
	}
	return store
}

func TestExpiringTokens(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	store := expiringFixture(t, now)
	cases := []struct {
		name   string
		within time.Duration
		want   []string
	}{
		{"window covers two expiring plus one expired", 15 * time.Minute, []string{"github-copilot", "openai", "通义千问"}},
		{"tight window excludes the two-minute token", time.Minute, []string{"github-copilot"}},
		{"zero window keeps only past-due tokens", 0, []string{"github-copilot"}},
		{"negative window clamps to zero", -5 * time.Minute, []string{"github-copilot"}},
		{"wide window still excludes the far-future token", time.Hour, []string{"github-copilot", "openai", "通义千问"}},
		{"very wide window includes the far-future token", 2 * time.Hour, []string{"github-copilot", "openai", "zai", "通义千问"}},
	}
	for _, test := range cases {
		got := ExpiringTokens(store, now, test.within)
		if !reflect.DeepEqual(got, test.want) {
			t.Fatalf("%s: ExpiringTokens(within=%v) = %v, want %v", test.name, test.within, got, test.want)
		}
	}
}

func TestExpiringTokensBoundary(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	store := scratchStore(t)
	if err := store.Put(Token{Provider: "edge", Kind: KindOAuth, Access: "a", ExpiresAt: now.Add(5 * time.Minute)}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got := ExpiringTokens(store, now, 5*time.Minute); !reflect.DeepEqual(got, []string{"edge"}) {
		t.Fatalf("exactly at the window edge should be included, got %v", got)
	}
	if got := ExpiringTokens(store, now, 5*time.Minute-time.Second); len(got) != 0 {
		t.Fatalf("one second outside the window should be excluded, got %v", got)
	}
}

func TestExpiringTokensRedactsSecrets(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	store := expiringFixture(t, now)
	got := ExpiringTokens(store, now, 2*time.Hour)

	secrets := []string{"access-gh", "refresh-gh", "access-oa", "refresh-oa", "access-cjk", "refresh-cjk", "sk-anthropic-key"}
	for _, id := range got {
		for _, secret := range secrets {
			if strings.Contains(id, secret) {
				t.Fatalf("result leaked secret %q in provider id %q", secret, id)
			}
		}
		if id != "github-copilot" && id != "openai" && id != "zai" && id != "通义千问" {
			t.Fatalf("unexpected non-provider entry %q", id)
		}
	}
}

func TestExpiringTokensSkipsEntryWithoutSecret(t *testing.T) {
	// Put 拒绝无秘密的令牌，但手动编辑的文件可能包含一个；扫描器必须跳过它而非报告不可用的凭据。
	// 令牌；扫描器必须跳过它而非报告不可用的凭据。
	now := time.Unix(1_800_000_000, 0).UTC()
	store := scratchStore(t)
	doc := `{"version":1,"tokens":[` +
		`{"provider":"no-secret","kind":"oauth","expires_at":1800000060},` +
		`{"provider":"有秘钥","kind":"oauth","access":"a","expires_at":1800000060}` +
		`]}`
	if err := os.WriteFile(store.Path(), []byte(doc), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got := ExpiringTokens(store, now, time.Minute)
	if !reflect.DeepEqual(got, []string{"有秘钥"}) {
		t.Fatalf("ExpiringTokens = %v, want only the secret-bearing token", got)
	}
}

func TestExpiringTokensEmptyStore(t *testing.T) {
	store := scratchStore(t)
	if got := ExpiringTokens(store, time.Unix(1_800_000_000, 0).UTC(), time.Hour); len(got) != 0 {
		t.Fatalf("empty store returned %v, want no entries", got)
	}
}

func TestExpiringTokensCorruptStoreReturnsNil(t *testing.T) {
	store := scratchStore(t)
	if err := os.WriteFile(store.Path(), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if got := ExpiringTokens(store, time.Unix(1_800_000_000, 0).UTC(), time.Hour); got != nil {
		t.Fatalf("corrupt store returned %v, want nil", got)
	}
}
