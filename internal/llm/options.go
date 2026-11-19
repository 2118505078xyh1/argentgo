package llm

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/url"
	"regexp"
	"strings"
)

// Wire protocol names. They match provider.Protocol's string values, but llm
// stays independent of the provider table.
const (
	ProtocolChatCompletions   = "openai-chat-completions"
	ProtocolAnthropicMessages = "anthropic-messages"
	ProtocolResponses         = "openai-responses"
)

// CacheControl marks a stable request prefix (system prompt plus tool schemas)
// for providers with explicit prompt caching. Type is normally "ephemeral";
// TTL is omitted unless a caller asks for a retention hint.
type CacheControl struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
}

// EphemeralCacheControl is the marker shared by the supported wire protocols.
func EphemeralCacheControl() *CacheControl {
	return &CacheControl{Type: "ephemeral"}
}

// Options are the wire-level knobs shared by every protocol client. The zero
// value keeps the historical wire format and auto-detects cache support from
// the protocol and endpoint.
type Options struct {
	// NoPromptCache mirrors --no-prompt-cache: no marker is emitted anywhere.
	// It wins over ForcePromptCache.
	NoPromptCache bool
	// ForcePromptCache opts an otherwise cache-free endpoint (local servers,
	// open-compatible chat completions) into stable-prefix markers.
	ForcePromptCache bool
	// CacheRetention, when set, adds a retention hint to markers and to the
	// Responses cache key request.
	CacheRetention string
	// IncludeUsage asks chat-completion providers for a final usage chunk.
	// Enabling prompt cache implies it.
	IncludeUsage bool
	// PromptCacheKey overrides the key derived from the stable prefix for the
	// Responses protocol.
	PromptCacheKey string
	// AttemptHook receives redacted attempt records for the session layer.
	AttemptHook func(AttemptRecord)
}

// CacheCapability describes the effective prompt-cache support of a Chatter.
type CacheCapability struct {
	Supported bool
	Protocol  string
	// Local reports that the endpoint is loopback and cache-free by default.
	Local bool
}

// PromptCacheCapable is implemented by clients that can advertise whether
// they emit cache markers. Use CacheSupport instead of importing a client type.
type PromptCacheCapable interface {
	PromptCacheCapability() CacheCapability
}

// CacheSupport reports prompt-cache support on the Chatter boundary. Chatters
// that do not implement PromptCacheCapable are reported as cache-free, which
// keeps scripted and third-party implementations unchanged.
func CacheSupport(ch Chatter) CacheCapability {
	if capable, ok := ch.(PromptCacheCapable); ok {
		return capable.PromptCacheCapability()
	}
	return CacheCapability{}
}

func (o Options) cacheEnabled(protocol, baseURL string) bool {
	if o.NoPromptCache {
		return false
	}
	if o.ForcePromptCache {
		return true
	}
	switch protocol {
	case ProtocolAnthropicMessages, ProtocolResponses:
		return !isLocalEndpoint(baseURL)
	default:
		// OpenAI-compatible chat completions have no standard marker; opt in
		// explicitly so local and open-compatible endpoints stay unchanged.
		return false
	}
}

func (o Options) cacheControl() *CacheControl {
	control := EphemeralCacheControl()
	control.TTL = strings.TrimSpace(o.CacheRetention)
	return control
}

// isLocalEndpoint treats loopback endpoints as local even when a provider
// template speaks a cache-capable protocol.
func isLocalEndpoint(baseURL string) bool {
	value := strings.TrimSpace(baseURL)
	if value == "" {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return strings.HasSuffix(host, ".localhost")
}

// deriveCacheKey hashes the stable prefix into a deterministic Responses
// prompt_cache_key. A short digest keeps request logs readable.
func deriveCacheKey(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = io.WriteString(hash, part)
		_, _ = hash.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))[:32]
}

var (
	credentialFieldRE = regexp.MustCompile(`(?i)(api[_-]?key|access[_-]?token|refresh[_-]?token|authorization|bearer|password|passwd|secret|token)(["']?\s*[:=]\s*["']?)([^\s"',;}]+)`)
	knownSecretRE     = regexp.MustCompile(`\b(?:sk|rk|pk|xoxb|ghp|gho|github_pat)_[A-Za-z0-9_\-]{12,}\b`)
	emailRE           = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	hexBlobRE         = regexp.MustCompile(`\b[A-Fa-f0-9]{32,}\b`)
)

// RedactText removes credential-shaped values from provider text before it is
// logged or attached to an attempt record. Ordinary error wording survives.
func RedactText(text string) string {
	text = credentialFieldRE.ReplaceAllString(text, "${1}${2}[redacted]")
	text = knownSecretRE.ReplaceAllString(text, "[redacted]")
	text = emailRE.ReplaceAllString(text, "[email]")
	text = hexBlobRE.ReplaceAllString(text, "[redacted]")
	return text
}

// RedactError is RedactText for errors; a nil error renders as an empty string.
func RedactError(err error) string {
	if err == nil {
		return ""
	}
	return RedactText(err.Error())
}
