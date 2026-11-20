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

// ThinkingConfig is the protocol-agnostic reasoning control carried by
// Options. It is a pure data carrier: no wire client reads it yet, so leaving
// Options.Thinking nil keeps every request byte-for-byte identical to earlier
// builds. The three fields cover the three kinds of knob vendors expose:
//   - Effort is a named level ("low"/"medium"/"high" or a CJK equivalent),
//     mapped to reasoning_effort / reasoning.effort by OpenAI-style protocols.
//   - BudgetTokens is an explicit thinking-token budget, mapped to Anthropic's
//     extended-thinking budget_tokens.
//   - Enabled is a plain boolean for vendors with only an on/off switch.
//
// A zero field means "no override for this knob"; it is the caller's job
// (currently internal/provider.ResolveThinking) to pick the fields a given
// protocol understands before this value reaches a wire client.
type ThinkingConfig struct {
	Effort       string `json:"effort,omitempty"`
	BudgetTokens int64  `json:"budget_tokens,omitempty"`
	Enabled      *bool  `json:"enabled,omitempty"`
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
	// PreserveReasoning opts in to replaying an assistant turn's reasoning on
	// later requests: reasoning_content on chat-completions and a thinking block
	// on Anthropic Messages. It is off by default so the bytes sent upstream —
	// and every existing wire-format test — stay exactly as before.
	PreserveReasoning bool
	// Thinking carries the normalized reasoning control for a future wire
	// client wiring step. It is nil by default and no wire client serializes it
	// yet, so adding this field cannot change any existing request bytes.
	Thinking *ThinkingConfig `json:"thinking,omitempty"`
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

// normalizeCacheTTL accepts only the retention tiers understood by the
// caching gateways. Whitespace is trimmed before matching, empty means "use
// the provider default", and any other value is rejected (returns "") rather
// than forwarded as an unsupported hint. It never mutates Options.
func normalizeCacheTTL(ttl string) string {
	trimmed := strings.TrimSpace(ttl)
	switch trimmed {
	case "", "5m", "30m", "1h", "24h":
		return trimmed
	default:
		return ""
	}
}

// normalizedCacheControl builds a marker for the advancing conversation-tail
// breakpoint. The long-standing stable-prefix markers keep using cacheControl
// unchanged so their wire format is preserved.
func (o Options) normalizedCacheControl() *CacheControl {
	control := EphemeralCacheControl()
	control.TTL = normalizeCacheTTL(o.CacheRetention)
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
	// authorizationRE swallows the whole value of an Authorization-style field
	// in one match: an optional scheme word (Bearer, Basic, ...) plus the token.
	// Matching the scheme and token together is essential -- matching only the
	// field name would redact "Bearer" and leave the real key behind. It runs
	// before credentialFieldRE so the generic field rule can never half-match
	// an Authorization header first.
	authorizationRE = regexp.MustCompile(`(?i)(authorization["']?\s*[:=]\s*["']?)([^\s"',;}]+(?:\s+[^\s"',;}]+)?)`)
	// Field names accept space, '_' or '-' between words, so "API key",
	// "api_key" and "api-key" are all covered. The trailing [:=] requirement
	// keeps ordinary prose from being treated as a credential field.
	credentialFieldRE = regexp.MustCompile(`(?i)(api[\s_\-]*key|access[\s_\-]*token|refresh[\s_\-]*token|authorization|bearer|password|passwd|secret|token)(["']?\s*[:=]\s*["']?)([^\s"',;}]+)`)
	// Known key prefixes use '-' or '_' as the separator. The value class also
	// contains '-'/'_', so multi-segment prefixes such as sk-ant-api03- and
	// sk-proj- are covered without listing every variant.
	knownSecretRE = regexp.MustCompile(`(?i)\b(?:sk|rk|pk|xoxb|ghp|gho|github_pat)[-_][A-Za-z0-9_\-]{12,}\b`)
	emailRE       = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	hexBlobRE     = regexp.MustCompile(`\b[A-Fa-f0-9]{32,}\b`)
)

// RedactText removes credential-shaped values from provider text before it is
// logged or attached to an attempt record. Ordinary error wording survives.
func RedactText(text string) string {
	text = authorizationRE.ReplaceAllString(text, "${1}[redacted]")
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
