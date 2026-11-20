package llm

import (
	"encoding/json"
	"strings"
)

// splitSSEData normalizes one SSE event's data payload into the JSON events it
// carries. The shared sseReader joins multi-line data: fields with newlines as
// the SSE spec requires. Two gateway shapes need extra handling:
//
//   - a single JSON object split across several data: lines (a proxy wrapping
//     long lines, or a split that falls inside a JSON string): removing the
//     synthetic newlines recovers valid JSON;
//   - several independent events emitted as adjacent data: lines with no blank
//     separator: each line is its own event and must be decoded in order.
//
// The common single-event case returns the payload unchanged in a one-element
// slice. Every protocol client funnels its payloads through this helper so they
// tolerate the same wire shapes.
func splitSSEData(data string) []string {
	if !strings.Contains(data, "\n") {
		return []string{data}
	}
	if compact := strings.ReplaceAll(data, "\n", ""); strings.HasPrefix(compact, "{") && json.Valid([]byte(compact)) {
		return []string{compact}
	}
	if lines := strings.Split(data, "\n"); allJSONValues(lines) {
		return lines
	}
	return []string{data}
}

// isEmptySSEData reports whether an SSE event's data payload carries no data.
// Gateways and load balancers legitimately send an empty "data:" line (or a
// whitespace-only one) as a keepalive on an otherwise idle stream. The SSE spec
// treats that as an event with no payload, not as a malformed JSON event, so
// every protocol client must skip it instead of aborting the reply with a
// "decode stream event" error.
func isEmptySSEData(data string) bool {
	return strings.TrimSpace(data) == ""
}

// allJSONValues reports whether every line is a standalone JSON value (or the
// [DONE] marker), which means the gateway omitted blank lines between what
// were meant to be separate events.
func allJSONValues(lines []string) bool {
	if len(lines) < 2 {
		return false
	}
	for _, line := range lines {
		if line == "[DONE]" {
			continue
		}
		if !json.Valid([]byte(line)) {
			return false
		}
	}
	return true
}
