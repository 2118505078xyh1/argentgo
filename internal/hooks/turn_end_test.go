package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestClassifyTurnEndEvent(t *testing.T) {
	cases := []struct {
		name     string
		canceled bool
		err      error
		want     string
	}{
		{name: "clean completion", want: "turn_end"},
		{name: "explicit cancellation flag", canceled: true, want: "interrupt"},
		{name: "context canceled sentinel", err: context.Canceled, want: "interrupt"},
		{name: "wrapped context canceled", err: fmt.Errorf("prompt: %w", context.Canceled), want: "interrupt"},
		{name: "doubly wrapped context canceled", err: fmt.Errorf("run: %w", fmt.Errorf("prompt: %w", context.Canceled)), want: "interrupt"},
		{name: "joined context canceled", err: errors.Join(errors.New("side effect"), context.Canceled), want: "interrupt"},
		{name: "canceled flag wins over a plain error", canceled: true, err: errors.New("boom"), want: "interrupt"},
		{name: "plain error is a failure", err: errors.New("provider 500"), want: "stop_failure"},
		{name: "wrapped error is a failure", err: fmt.Errorf("stream: %w", errors.New("EOF")), want: "stop_failure"},
		{name: "deadline exceeded is a failure", err: context.DeadlineExceeded, want: "stop_failure"},
		{name: "wrapped deadline exceeded is a failure", err: fmt.Errorf("wait: %w", context.DeadlineExceeded), want: "stop_failure"},
		{name: "text context canceled is a failure", err: errors.New("context canceled"), want: "stop_failure"},
		{name: "joined non-cancel errors stay a failure", err: errors.Join(errors.New("a"), errors.New("b")), want: "stop_failure"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := ClassifyTurnEndEvent(test.canceled, test.err); got != test.want {
				t.Fatalf("ClassifyTurnEndEvent(%v, %v) = %q, want %q", test.canceled, test.err, got, test.want)
			}
		})
	}
}

func TestClassifyTurnEndEventReasonsAreTheEventNames(t *testing.T) {
	// The classifier returns the event name, so callers can emit the result
	// directly as an Event without a mapping table.
	reasons := []struct {
		reason string
		event  Event
	}{
		{"interrupt", EventInterrupt},
		{"stop_failure", EventStopFailure},
		{"turn_end", EventTurnEnd},
	}
	for _, test := range reasons {
		if test.reason != string(test.event) {
			t.Fatalf("reason %q does not match event %q", test.reason, test.event)
		}
		found := false
		for _, event := range AllEvents {
			if event == test.event {
				found = true
			}
		}
		if !found {
			t.Fatalf("event %q missing from AllEvents", test.event)
		}
	}

	// Every reason is actually reachable from the classifier.
	seen := map[string]bool{}
	seen[ClassifyTurnEndEvent(true, nil)] = true
	seen[ClassifyTurnEndEvent(false, errors.New("x"))] = true
	seen[ClassifyTurnEndEvent(false, nil)] = true
	if len(seen) != len(reasons) {
		t.Fatalf("expected %d distinct reasons, got %v", len(reasons), seen)
	}
}

func TestTurnEndEventsAreSupportedAndStopIsRetained(t *testing.T) {
	// All three new events parse, land on the whitelist and survive a full
	// document round trip — as does the legacy Stop event.
	document := []byte(`{"hooks":{
		"Stop":[{"command":"echo stop"}],
		"interrupt":[{"command":"echo i"}],
		"stop_failure":[{"command":"echo f"}],
		"turn_end":[{"command":"echo e"}]
	}}`)
	if warnings := Validate(document); len(warnings) != 0 {
		t.Fatalf("supported events reported as unknown: %v", warnings)
	}
	config, err := ParseConfig(document)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, event := range []Event{Stop, EventInterrupt, EventStopFailure, EventTurnEnd} {
		parsed, ok := ParseEvent(event.String())
		if !ok || parsed != event {
			t.Fatalf("event %q did not round trip", event)
		}
		if got := config.ForEvent(event, ""); len(got) != 1 {
			t.Fatalf("event %q lost its command: %+v", event, got)
		}
	}
	if _, ok := ParseEvent("Interrupt"); ok {
		t.Fatal("the event name is lowercase; a capitalized near-miss must not parse")
	}
}

func TestHookPayloadFieldIsOptionalAndZeroValueCompatible(t *testing.T) {
	// A payload without HookPayload keeps its exact JSON shape: adding the
	// field must not change any existing serialized document.
	encoded, err := json.Marshal(Payload{Event: "Stop", Prompt: "hi", Output: "bye"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), "hookPayload") {
		t.Fatalf("empty HookPayload must be omitted: %s", encoded)
	}

	// A populated HookPayload round trips.
	withPayload := Payload{Event: string(EventStopFailure), HookPayload: "provider 500"}
	encoded, err = json.Marshal(withPayload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(fields["hookPayload"]) != `"provider 500"` {
		t.Fatalf("HookPayload lost: %s", encoded)
	}
	var decoded Payload
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.HookPayload != withPayload.HookPayload {
		t.Fatalf("HookPayload round trip: %+v", decoded)
	}
}
