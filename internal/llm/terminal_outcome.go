package llm

import (
	"net/http"
	"regexp"
)

// maxTerminalScanBytes bounds how much of a response body the terminal-outcome
// classifier examines. Provider error payloads are small; a pathological or
// hostile body must not make classification unbounded.
const maxTerminalScanBytes = 64 * 1024

// terminalOutcomePatterns lists provider-reported outcomes that cannot be
// settled by retrying: the bill cannot be settled, the accounting record cannot
// be produced, or a policy/hook has already denied the request. They mirror the
// terminal classes Codex refuses to retry. Patterns are matched
// case-insensitively and tolerate '_', '-' and spaces between words so both
// JSON error codes and human-readable messages are recognised.
//
// Order matters: more specific codes precede their shorter prefixes, so
// TerminalOutcomeReason reports the precise reason (spend_limit_exceeded rather
// than spend_limit).
var terminalOutcomePatterns = []struct {
	reason string
	re     *regexp.Regexp
}{
	{"session_budget_exceeded", regexp.MustCompile(`(?i)session[\s_-]*budget[\s_-]*exceeded`)},
	{"organization_budget_exceeded", regexp.MustCompile(`(?i)organization[\s_-]*budget[\s_-]*exceeded`)},
	{"spend_limit_exceeded", regexp.MustCompile(`(?i)spend[\s_-]*limit[\s_-]*exceeded`)},
	{"spend_limit", regexp.MustCompile(`(?i)spend[\s_-]*limit`)},
	{"usage_accounting_failed", regexp.MustCompile(`(?i)usage[\s_-]*accounting[\s_-]*failed`)},
	{"inference_hook_denied", regexp.MustCompile(`(?i)inference[\s_-]*hook[\s_-]*denied`)},
	{"billing_hard_limit_reached", regexp.MustCompile(`(?i)billing[\s_-]*hard[\s_-]*limit[\s_-]*reached`)},
	{"insufficient_quota", regexp.MustCompile(`(?i)insufficient[\s_-]*quota`)},
}

// IsTerminalProviderOutcome reports whether a provider response describes a
// terminal outcome: retrying it cannot clear the condition (the session or
// organization budget is spent, the spend limit is hit, usage accounting
// failed, an inference hook denied the request, and so on). Such a response
// must be handed back to the caller with its body intact rather than waited
// out, because no amount of backoff will make it succeed.
//
// body is expected to be an already-redacted response body (callers pass a
// bounded prefix). This classifier never logs or echoes the body; the only
// strings it returns are the fixed canonical reasons below. Redaction is
// re-applied defensively so a secret that happens to contain a pattern word
// cannot create a false positive. Scanning is capped at maxTerminalScanBytes
// so a pathological body stays bounded. HTTP 402 (Payment Required) is
// terminal on its own, even with an empty or unrecognised body.
func IsTerminalProviderOutcome(body []byte, statusCode int) bool {
	return TerminalOutcomeReason(body, statusCode) != ""
}

// TerminalOutcomeReason returns the canonical reason string for a terminal
// provider outcome, or "" when the response is not terminal. Callers may
// include the returned reason in a user-facing message without ever exposing
// the raw body.
func TerminalOutcomeReason(body []byte, statusCode int) string {
	// Bound the scan before any allocation or matching.
	if len(body) > maxTerminalScanBytes {
		body = body[:maxTerminalScanBytes]
	}
	if len(body) > 0 {
		// Defensive redaction: a credential-shaped value must not influence the
		// decision. Normal callers already pass a redacted body.
		text := RedactText(string(body))
		for _, p := range terminalOutcomePatterns {
			if p.re.MatchString(text) {
				return p.reason
			}
		}
	}
	if statusCode == http.StatusPaymentRequired {
		return "payment_required"
	}
	return ""
}
