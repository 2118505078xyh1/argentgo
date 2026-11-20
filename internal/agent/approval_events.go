package agent

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// Approval events are built on EventNotice (see events.go): the event-kind set
// is owned by that file, so rather than add a new kind we reserve a stable text
// prefix and carry a structured JSON payload in Event.Text. That keeps these
// items on the ordinary agent stream, which means every transport — the
// in-process Emit callback, --rpc and the native bridge (which only forwards
// text deltas/notices) — delivers them without any protocol change.
//
// Wire contract for the native bridge (and any other text-only consumer):
//
//	approval:request  {"v":1,"tool":"<tool>","digest":"<hash>","id":"<id>"}
//	approval:resolved {"v":1,"tool":"<tool>","digest":"","id":"<id>","granted":true|false}
//
// The prefix is followed immediately by a JSON object. "v" is the schema
// version (currently 1). "id" correlates a resolved event back to its request
// (callers normally pass the originating tool call id). "digest" is a stable
// hash of the exact arguments and is always redacted/bounded. "granted" is
// present only on resolved events. ParseApprovalEvent is the single decoder;
// it returns ok=false for unrelated notices, bad prefixes and malformed
// payloads so consumers can ignore everything else safely.
const (
	approvalRequestPrefix  = "approval:request "
	approvalResolvedPrefix = "approval:resolved "
	approvalWireVersion    = 1

	// approvalFieldMaxRunes caps caller-supplied tool names, argument digests
	// and correlation ids. A hex sha256 digest is 64 runes, so honest inputs
	// pass through unchanged; the cap only bites on malformed/hostile values.
	approvalFieldMaxRunes = 128
)

// approvalWire is the JSON payload shared by both event constructors and the
// parser. Granted is a pointer so the decoder can tell "denied" (false) from a
// missing field (nil => malformed).
type approvalWire struct {
	V       int    `json:"v"`
	Tool    string `json:"tool"`
	Digest  string `json:"digest"`
	ID      string `json:"id"`
	Granted *bool  `json:"granted,omitempty"`
}

// ApprovalRequestedEvent builds a notice announcing that approval is being
// asked for tool. argsDigest should be a stable hash of the exact arguments
// (e.g. a hex sha256); it is defensively bounded and stripped of control bytes
// so a hostile value cannot inject an extra transcript line. id correlates the
// later resolved event back to this request (normally the tool call id).
func ApprovalRequestedEvent(tool, argsDigest, id string) Event {
	// Marshaling a struct of only strings/bools/ints cannot fail.
	payload, _ := json.Marshal(approvalWire{
		V:      approvalWireVersion,
		Tool:   redactApprovalField(tool),
		Digest: redactApprovalField(argsDigest),
		ID:     redactApprovalField(id),
	})
	return Event{Kind: EventNotice, Text: approvalRequestPrefix + string(payload)}
}

// ApprovalResolvedEvent builds a notice recording the outcome of the approval
// request identified by id. tool and id must match the request event so a
// consumer can pair the two into one transcript item.
func ApprovalResolvedEvent(tool, id string, granted bool) Event {
	// Marshaling a struct of only strings/bools/ints cannot fail.
	payload, _ := json.Marshal(approvalWire{
		V:       approvalWireVersion,
		Tool:    redactApprovalField(tool),
		ID:      redactApprovalField(id),
		Granted: &granted,
	})
	return Event{Kind: EventNotice, Text: approvalResolvedPrefix + string(payload)}
}

// ParseApprovalEvent decodes an event produced by ApprovalRequestedEvent or
// ApprovalResolvedEvent. kind is "request" or "resolved"; granted is only
// meaningful for "resolved". ok is false for any other event kind, an unknown
// prefix or a malformed payload, so a consumer can ignore unrelated notices.
func ParseApprovalEvent(e Event) (kind, tool, id string, granted bool, ok bool) {
	if e.Kind != EventNotice {
		return "", "", "", false, false
	}
	switch {
	case strings.HasPrefix(e.Text, approvalRequestPrefix):
		var p approvalWire
		if err := json.Unmarshal([]byte(strings.TrimPrefix(e.Text, approvalRequestPrefix)), &p); err != nil {
			return "", "", "", false, false
		}
		if p.V != approvalWireVersion || p.Tool == "" || p.ID == "" {
			return "", "", "", false, false
		}
		return "request", p.Tool, p.ID, false, true
	case strings.HasPrefix(e.Text, approvalResolvedPrefix):
		var p approvalWire
		if err := json.Unmarshal([]byte(strings.TrimPrefix(e.Text, approvalResolvedPrefix)), &p); err != nil {
			return "", "", "", false, false
		}
		if p.V != approvalWireVersion || p.Tool == "" || p.ID == "" || p.Granted == nil {
			return "", "", "", false, false
		}
		return "resolved", p.Tool, p.ID, *p.Granted, true
	default:
		return "", "", "", false, false
	}
}

// redactApprovalField drops control bytes and ANSI escape sequences so a value
// can never inject an extra transcript line or escape codes, then truncates on
// a rune boundary with an ellipsis marker so the total stays within
// approvalFieldMaxRunes. (The wire payload is JSON-encoded, so newlines are
// already escaped; stripping is defence in depth for text-only consumers.)
func redactApprovalField(s string) string {
	s = stripApprovalControls(s)
	if utf8.RuneCountInString(s) <= approvalFieldMaxRunes {
		return s
	}
	return truncateApprovalRunes(s, approvalFieldMaxRunes-1) + "…"
}

// stripApprovalControls removes ANSI CSI/OSC/bare-ESC sequences and control
// bytes, keeping printable text. It never emits a newline or NUL.
func stripApprovalControls(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		if c == 0x1b { // ESC: consume the whole escape sequence.
			i++
			if i >= len(s) {
				break
			}
			switch s[i] {
			case '[': // CSI: through a final byte in 0x40..0x7e.
				i++
				for i < len(s) {
					last := s[i]
					i++
					if last >= 0x40 && last <= 0x7e {
						break
					}
				}
			case ']': // OSC: through BEL (0x07) or ST (ESC '\').
				i++
				for i < len(s) {
					if s[i] == 0x07 {
						i++
						break
					}
					if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
						i += 2
						break
					}
					i++
				}
			default: // Other escapes: drop ESC and this one byte.
				i++
			}
			continue
		}
		if c < 0x20 || c == 0x7f { // ASCII control byte, including \n and \r.
			i++
			continue
		}
		if c < 0x80 {
			b.WriteByte(c)
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r >= 0x80 && r <= 0x9f { // C1 control byte encoded in UTF-8.
			i += size
			continue
		}
		b.WriteString(s[i : i+size])
		i += size
	}
	return b.String()
}

// truncateApprovalRunes returns the first max runes of s, never splitting a
// UTF-8 sequence.
func truncateApprovalRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	kept := 0
	for pos := range s {
		if kept == max {
			return s[:pos]
		}
		kept++
	}
	return s
}
