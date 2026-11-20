package app

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// formatInspectionReason renders one inspection verdict's finding ID and
// reason as a single, human-readable context string. It is pure: no I/O, no
// mutation and no dependency on an approver. Reasons and finding IDs returned
// by InspectionManager.Resolve are already redacted and rune-bounded, so this
// function only composes them — it never re-introduces raw argument text.
//
// The string is intended as display context for the operator ("why am I being
// asked?"), never as an input to a cache key, permission scope or audit
// record. See askInspectionApproval for why those inputs stay unchanged.
//
//	reason + id -> "[security SEC-1234: destructive shell command: rm -rf /]"
//	reason only -> "[security: destructive shell command: rm -rf /]"
//	id only     -> "[security SEC-1234]"
//	neither     -> "" (there is nothing to explain)
func formatInspectionReason(v security.Verdict) string {
	reason := oneLine(strings.TrimSpace(v.Reason))
	id := oneLine(strings.TrimSpace(v.FindingID))
	switch {
	case reason == "" && id == "":
		return ""
	case id == "":
		return "[security: " + reason + "]"
	case reason == "":
		return "[security " + id + "]"
	default:
		return "[security " + id + ": " + reason + "]"
	}
}

// oneLine folds a verdict string into one printable line so a multi-line
// reason cannot break an approval prompt. It preserves every printable rune
// (including CJK) and only replaces horizontal whitespace with a space and
// drops ASCII control bytes. UTF-8 sequences are never split because the loop
// ranges over runes.
func oneLine(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteByte(' ')
		case r < 0x20 || r == 0x7f:
			// Drop other ASCII control bytes; they must never reach a prompt.
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// reasonAwareApprover is an optional capability an Approver may implement to
// receive an inspection finding's reason as display context alongside the
// tool name and arguments. Implementations MUST derive cache keys, permission
// scopes and audit records only from name and args, never from reason: the
// reason can change between two otherwise identical calls, and folding it into
// any key would make approval behaviour and audit drift from the call that was
// actually authorised.
//
// No approver in this repository implements this interface today, so adding it
// is a deliberate no-op for current runs. It is the safe wiring point for a
// future security-side Ask variant that carries a reason (that package is
// owned by a separate change); until then the reason is formatted and handed
// to this seam, and plain Ask is used.
type reasonAwareApprover interface {
	AskWithReason(ctx context.Context, name, args, reason string) (bool, error)
}

// askInspectionApproval asks the operator once for an InspectRequireApproval
// verdict, attaching the verdict's finding context when the approver supports
// it. Crucially it passes name and args through UNCHANGED. The guard shares
// its approver with security.Chain, and CachedApprover / ttlApprover /
// ScopedApprover all key their grants (and ScopedApprover its deny/always
// levels) on name + canonical args. Appending a "[security: ...]" prefix to
// either would create a different cache key and permission scope for the same
// tool call, so it is intentionally not done: the reason travels only through
// the optional AskWithReason seam, which keeps it out of every key.
func askInspectionApproval(ctx context.Context, approver security.Approver, tool string, args json.RawMessage, v security.Verdict) (bool, error) {
	if approver == nil {
		// Mirror Chain.check's null-object behaviour rather than panicking.
		return security.UnavailableApprover{}.Ask(ctx, tool, string(args))
	}
	if aware, ok := approver.(reasonAwareApprover); ok {
		return aware.AskWithReason(ctx, tool, string(args), formatInspectionReason(v))
	}
	return approver.Ask(ctx, tool, string(args))
}
