package auth

import "time"

// ExpiringTokens lists the provider IDs whose stored credential is due to
// expire within the next `within` window, including credentials that have
// already expired. It is a read-only diagnostic aid: the result contains only
// provider IDs, never an access or refresh secret, so it is safe to log, show
// in a UI, or drop into a support bundle.
//
// A credential counts as expiring while its remaining lifetime is at most
// `within`; a non-positive `within` therefore narrows the scan to credentials
// already past their deadline at `now`. A negative `within` behaves like zero.
// Entries with no deadline (API keys) and entries with no secret are skipped,
// since they never expire. `now` is explicit so callers and tests share a
// clock, and classification reuses TokenStatus so the margin and overflow
// rules stay identical across the package.
//
// Results follow the store's provider order. If the store cannot be read,
// ExpiringTokens returns nil rather than a silently empty list, so a corrupt
// credential file is never mistaken for a clean bill of health.
func ExpiringTokens(store *Store, now time.Time, within time.Duration) []string {
	tokens, err := store.List()
	if err != nil {
		return nil
	}
	margin := int64(within / time.Second)
	out := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if token.Bearer() == "" || token.ExpiresAt.IsZero() {
			continue
		}
		switch state := TokenStatus(token.ExpiresAt.Unix(), now.Unix(), margin); state {
		case TokenExpiring, TokenExpired:
			out = append(out, token.Provider)
		}
	}
	return out
}
