package agent

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestApprovalEventRoundtrip(t *testing.T) {
	tests := []struct {
		name       string
		event      Event
		wantKind   string
		wantTool   string
		wantID     string
		wantPrefix string
		wantGrant  bool
	}{
		{
			name:       "request",
			event:      ApprovalRequestedEvent("bash", "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08", "call_42"),
			wantKind:   "request",
			wantTool:   "bash",
			wantID:     "call_42",
			wantPrefix: approvalRequestPrefix,
		},
		{
			name:       "resolved_granted",
			event:      ApprovalResolvedEvent("bash", "call_42", true),
			wantKind:   "resolved",
			wantTool:   "bash",
			wantID:     "call_42",
			wantPrefix: approvalResolvedPrefix,
			wantGrant:  true,
		},
		{
			name:       "resolved_denied",
			event:      ApprovalResolvedEvent("write", "call_9", false),
			wantKind:   "resolved",
			wantTool:   "write",
			wantID:     "call_9",
			wantPrefix: approvalResolvedPrefix,
			wantGrant:  false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.event.Kind != EventNotice {
				t.Fatalf("kind = %q, want %q", tc.event.Kind, EventNotice)
			}
			if !strings.HasPrefix(tc.event.Text, tc.wantPrefix) {
				t.Fatalf("text %q does not start with %q", tc.event.Text, tc.wantPrefix)
			}
			kind, tool, id, granted, ok := ParseApprovalEvent(tc.event)
			if !ok {
				t.Fatalf("ParseApprovalEvent ok = false for %q", tc.event.Text)
			}
			if kind != tc.wantKind || tool != tc.wantTool || id != tc.wantID || granted != tc.wantGrant {
				t.Fatalf("got (%q, %q, %q, %v), want (%q, %q, %q, %v)",
					kind, tool, id, granted, tc.wantKind, tc.wantTool, tc.wantID, tc.wantGrant)
			}
		})
	}
}

func TestParseApprovalEventRejectsNonApprovalEvents(t *testing.T) {
	tests := []struct {
		name  string
		event Event
	}{
		{"text_delta", Event{Kind: EventTextDelta, Text: approvalRequestPrefix + `{"v":1,"tool":"bash","digest":"x","id":"c"}`}},
		{"plain_notice", Event{Kind: EventNotice, Text: "compacted context"}},
		{"empty_notice", Event{Kind: EventNotice}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, _, _, ok := ParseApprovalEvent(tc.event); ok {
				t.Fatalf("ParseApprovalEvent ok = true for non-approval event %+v", tc.event)
			}
		})
	}
}

func TestParseApprovalEventRejectsMalformedPrefixAndPayload(t *testing.T) {
	tests := []struct {
		name string
		text string
	}{
		{"unknown_verb", "approval:unknown "},
		{"missing_space", "approval:request"},
		{"bad_json", approvalRequestPrefix + "not json"},
		{"empty_payload", approvalRequestPrefix},
		{"wrong_version", approvalRequestPrefix + `{"v":2,"tool":"bash","digest":"x","id":"c"}`},
		{"missing_tool", approvalRequestPrefix + `{"v":1,"digest":"x","id":"c"}`},
		{"missing_id", approvalRequestPrefix + `{"v":1,"tool":"bash","digest":"x"}`},
		{"resolved_without_granted", approvalResolvedPrefix + `{"v":1,"tool":"bash","id":"c"}`},
		{"resolved_wrong_version", approvalResolvedPrefix + `{"v":9,"tool":"bash","id":"c","granted":true}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := Event{Kind: EventNotice, Text: tc.text}
			if _, _, _, _, ok := ParseApprovalEvent(e); ok {
				t.Fatalf("ParseApprovalEvent ok = true for malformed text %q", tc.text)
			}
		})
	}
}

func TestApprovalEventBoundsAndRedactsFields(t *testing.T) {
	long := strings.Repeat("A", approvalFieldMaxRunes+50)
	// 嵌入控制字节和换行，它们绝不能进入记录。
	hostile := "bash\n\x1b[31m\x00rm"
	digest := long

	request := ApprovalRequestedEvent(hostile, digest, "call_ok")
	kind, tool, _, _, ok := ParseApprovalEvent(request)
	if !ok {
		t.Fatal("request event did not parse")
	}
	if kind != "request" || tool != "bashrm" {
		t.Fatalf("tool = %q, want control bytes stripped to %q", tool, "bashrm")
	}

	// ParseApprovalEvent 不返回 digest 字段；改为通过原始文本往返，在在线负载上检查它。
	var wire approvalWire
	payload := strings.TrimPrefix(request.Text, approvalRequestPrefix)
	if err := json.Unmarshal([]byte(payload), &wire); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if got := utf8.RuneCountInString(wire.Digest); got > approvalFieldMaxRunes {
		t.Fatalf("digest rune count = %d, want <= %d", got, approvalFieldMaxRunes)
	}
	if !strings.HasSuffix(wire.Digest, "…") {
		t.Fatalf("overlong digest was not truncated with a marker: %q", wire.Digest)
	}
	if strings.ContainsAny(request.Text, "\n\r\x00") {
		t.Fatalf("event text contains an injected control byte: %q", request.Text)
	}

	// 过长的工具名也会被限长。
	_, boundedTool, _, _, ok := ParseApprovalEvent(ApprovalRequestedEvent(long, "digest", "id"))
	if !ok {
		t.Fatal("bounded request event did not parse")
	}
	if got := utf8.RuneCountInString(boundedTool); got > approvalFieldMaxRunes {
		t.Fatalf("tool rune count = %d, want <= %d", got, approvalFieldMaxRunes)
	}
}

func TestApprovalEventsCarryCorrelatingID(t *testing.T) {
	const id = "call_abc-123"
	request := ApprovalRequestedEvent("bash", "hash", id)
	resolved := ApprovalResolvedEvent("bash", id, true)

	_, reqTool, reqID, _, reqOK := ParseApprovalEvent(request)
	_, resTool, resID, granted, resOK := ParseApprovalEvent(resolved)
	if !reqOK || !resOK {
		t.Fatalf("parse failed: request=%v resolved=%v", reqOK, resOK)
	}
	if reqTool != resTool || reqID != resID || reqID != id {
		t.Fatalf("request/resolved do not correlate: (%q,%q) vs (%q,%q)", reqTool, reqID, resTool, resID)
	}
	if !granted {
		t.Fatal("resolved event lost the granted flag")
	}
}
