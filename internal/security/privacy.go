package security

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// PrivacyMode is the network posture selected by --privacy-mode. It is
// independent of SandboxMode: sandbox modes govern file tools, privacy modes
// govern remote endpoints and network tools.
type PrivacyMode string

const (
	// PrivacyLocalOnly blocks remote endpoints and web tools by default. It is
	// the deterministic, auditable posture for sensitive work.
	PrivacyLocalOnly PrivacyMode = "local-only"
	// PrivacyAny keeps the historical behavior: every endpoint is reachable.
	PrivacyAny PrivacyMode = "any"
)

// maxPrivacyURLBytes bounds what the privacy check will parse. Values arrive
// from layered config files, so an unbounded string must never reach net/url.
const maxPrivacyURLBytes = 4096

var (
	ErrPrivacyEmptyURL     = errors.New("security: privacy: empty URL")
	ErrPrivacyURLTooLong   = errors.New("security: privacy: URL too long")
	ErrPrivacyMalformedURL = errors.New("security: privacy: malformed URL")
	ErrPrivacyScheme       = errors.New("security: privacy: scheme is not http or https")
	ErrPrivacyNoHost       = errors.New("security: privacy: URL has no host")
)

// Decision reasons are stable constants: no dynamic input goes into them so
// the audit log stays deterministic and safe to diff.
const (
	reasonPrivacyAny           = "privacy: mode=any: remote endpoints allowed"
	reasonPrivacyLoopback      = "privacy: mode=local-only: loopback endpoint allowed"
	reasonPrivacyOverride      = "privacy: mode=local-only: remote endpoint allowed by explicit override"
	reasonPrivacyRemoteBlocked = "privacy: mode=local-only: remote endpoint blocked"
	reasonPrivacyInvalidURL    = "privacy: mode=local-only: endpoint denied (invalid URL)"
	reasonPrivacyUnknownMode   = "privacy: unknown mode: endpoint denied"
)

// IsLoopbackURL reports whether rawURL is an http(s) URL whose host resolves
// to the local machine: localhost, any 127.0.0.0/8 address, or ::1. A valid
// but remote URL returns (false, nil); anything that cannot be parsed as an
// http(s) URL returns (false, error) so callers can fail closed.
func IsLoopbackURL(rawURL string) (bool, error) {
	if len(rawURL) > maxPrivacyURLBytes {
		return false, fmt.Errorf("%w: %d bytes", ErrPrivacyURLTooLong, len(rawURL))
	}
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return false, ErrPrivacyEmptyURL
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrPrivacyMalformedURL, err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return false, fmt.Errorf("%w: %q", ErrPrivacyScheme, parsed.Scheme)
	}
	host := parsed.Hostname()
	if host == "" {
		return false, ErrPrivacyNoHost
	}
	if strings.EqualFold(host, "localhost") {
		return true, nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false, nil
	}
	return ip.IsLoopback(), nil
}

// AllowRemote applies a privacy mode to one endpoint. An "any" mode allows
// everything; "local-only" allows loopback endpoints or an explicit override.
// Unknown modes are denied so a typo cannot silently widen the posture. The
// returned reason is the stable string callers should put in the audit entry.
func AllowRemote(mode PrivacyMode, rawURL string, allowRemoteOverride bool) (bool, string) {
	switch mode {
	case PrivacyAny:
		return true, reasonPrivacyAny
	case PrivacyLocalOnly:
		// Handled below.
	default:
		return false, reasonPrivacyUnknownMode
	}
	if allowRemoteOverride {
		return true, reasonPrivacyOverride
	}
	loopback, err := IsLoopbackURL(rawURL)
	if err != nil {
		return false, reasonPrivacyInvalidURL
	}
	if loopback {
		return true, reasonPrivacyLoopback
	}
	return false, reasonPrivacyRemoteBlocked
}

// ShouldBlockWebTool reports the posture default for web_search/web_fetch. An
// explicit allow-remote override is a separate caller concern: the helper only
// reports what the mode itself requires, keeping the decision in one place.
func ShouldBlockWebTool(mode PrivacyMode) bool {
	return mode == PrivacyLocalOnly
}

// PrivacyAuditReason returns the stable "privacy_mode" event reason used for
// audit entries. An empty mode is recorded as the default "any" posture so the
// event is always self-describing. changed marks a transition from the default.
func PrivacyAuditReason(mode PrivacyMode, changed bool) string {
	value := string(mode)
	if value == "" {
		value = string(PrivacyAny)
	}
	return fmt.Sprintf("privacy_mode=%s changed=%t", value, changed)
}
