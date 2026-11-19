package llm

import (
	"net/http"
	"strings"
)

// Auth is one resolved auth header. A zero-value Auth means "no auth": it
// writes no header, which is how apiKey:false / NoAuth is represented.
type Auth struct {
	Header string
	Value  Secret
}

// NoAuth returns the sentinel used when a provider needs no credential.
func NoAuth() Auth {
	return Auth{}
}

// OrElse returns the receiver when it carries a credential, otherwise the
// fallback. Chaining it models an ordered list of env var sources.
func (a Auth) OrElse(fallback Auth) Auth {
	if a.Value.Value() != "" {
		return a
	}
	return fallback
}

// Apply writes the resolved header. NoAuth deliberately writes nothing.
func (a Auth) Apply(headers http.Header) {
	if headers == nil || a.Header == "" || a.Value.Value() == "" {
		return
	}
	headers.Set(a.Header, a.Value.Value())
}

// ResolveCredential returns the first non-empty value from names. An empty
// name is skipped; whitespace-only values count as missing.
func ResolveCredential(lookup func(string) string, names []string) string {
	for _, name := range names {
		if strings.TrimSpace(name) == "" || lookup == nil {
			continue
		}
		if value := strings.TrimSpace(lookup(name)); value != "" {
			return value
		}
	}
	return ""
}

// ResolveAuth builds an auth header from the first non-empty credential in
// names. scheme is prepended when set ("Bearer" for Authorization); an empty
// scheme sends the raw value (x-api-key). No names or no value yields NoAuth.
func ResolveAuth(lookup func(string) string, names []string, header, scheme string) Auth {
	credential := ResolveCredential(lookup, names)
	if credential == "" {
		return NoAuth()
	}
	if scheme != "" {
		credential = scheme + " " + credential
	}
	return Auth{Header: header, Value: NewSecret(credential)}
}
