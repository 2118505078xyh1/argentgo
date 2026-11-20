package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"testing"
)

// TestPermissionOptions checks the four canonical ACP permission choices:
// their optionId, kind and human-readable name must stay in sync.
func TestPermissionOptions(t *testing.T) {
	want := []PermissionOption{
		{OptionID: "allow_once", Kind: PermissionOptionAllowOnce, Name: "Allow once"},
		{OptionID: "allow_always", Kind: PermissionOptionAllowAlways, Name: "Always allow"},
		{OptionID: "reject_once", Kind: PermissionOptionRejectOnce, Name: "Reject once"},
		{OptionID: "reject_always", Kind: PermissionOptionRejectAlways, Name: "Always reject"},
	}
	got := permissionOptions()
	if len(got) != len(want) {
		t.Fatalf("permissionOptions() returned %d options, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("permissionOptions()[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestPermissionOptionKindNames pins the name text of each kind independently
// of option ordering, so a future reorder cannot silently change the labels.
func TestPermissionOptionKindNames(t *testing.T) {
	byKind := make(map[PermissionOptionKind]string)
	for _, opt := range permissionOptions() {
		byKind[opt.Kind] = opt.Name
	}
	want := map[PermissionOptionKind]string{
		PermissionOptionAllowOnce:    "Allow once",
		PermissionOptionAllowAlways:  "Always allow",
		PermissionOptionRejectOnce:   "Reject once",
		PermissionOptionRejectAlways: "Always reject",
	}
	if len(byKind) != len(want) {
		t.Fatalf("names by kind = %v, want %d distinct kinds", byKind, len(want))
	}
	for kind, name := range want {
		if byKind[kind] != name {
			t.Errorf("name for kind %q = %q, want %q", kind, byKind[kind], name)
		}
	}
}

// TestPermissionOutcome checks the option -> outcome mapping, including the
// fail-closed cases (empty, unknown and wrongly-cased choices all cancel).
func TestPermissionOutcome(t *testing.T) {
	tests := []struct {
		name     string
		optionID string
		want     RequestPermissionOutcome
	}{
		{"allow once", "allow_once", RequestPermissionOutcome{Outcome: "selected", OptionID: "allow_once"}},
		{"allow always", "allow_always", RequestPermissionOutcome{Outcome: "selected", OptionID: "allow_always"}},
		{"reject once", "reject_once", RequestPermissionOutcome{Outcome: "selected", OptionID: "reject_once"}},
		{"reject always", "reject_always", RequestPermissionOutcome{Outcome: "selected", OptionID: "reject_always"}},
		{"empty option", "", RequestPermissionOutcome{Outcome: "cancelled"}},
		{"unknown option", "allow_forever", RequestPermissionOutcome{Outcome: "cancelled"}},
		{"wrong case", "ALLOW_ONCE", RequestPermissionOutcome{Outcome: "cancelled"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := permissionOutcome(tc.optionID); got != tc.want {
				t.Fatalf("permissionOutcome(%q) = %+v, want %+v", tc.optionID, got, tc.want)
			}
		})
	}
}

// acpPermissionRequest drives one session/request_permission frame through a
// fresh transport and returns its single reply. When sendParams is false the
// "params" key is omitted entirely (the ACP "no option" case).
func acpPermissionRequest(t *testing.T, params map[string]any, sendParams bool) map[string]any {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- RunACP(context.Background(), inR, outW, Config{Runner: &fakeRunner{}})
	}()

	msg := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "session/request_permission"}
	if sendParams {
		msg["params"] = params
	}
	if err := json.NewEncoder(inW).Encode(msg); err != nil {
		t.Fatalf("encode request: %v", err)
	}
	resp := readJSON(t, json.NewDecoder(outR))

	_ = inW.Close()
	waitFor(t, done)
	return resp
}

// acpPermissionRawRequest sends a pre-built frame, for cases a Go map cannot
// express (e.g. a "params" value of the wrong JSON type).
func acpPermissionRawRequest(t *testing.T, frame string) map[string]any {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- RunACP(context.Background(), inR, outW, Config{Runner: &fakeRunner{}})
	}()

	if _, err := fmt.Fprintln(inW, frame); err != nil {
		t.Fatalf("write request: %v", err)
	}
	resp := readJSON(t, json.NewDecoder(outR))

	_ = inW.Close()
	waitFor(t, done)
	return resp
}

// TestACPRequestPermissionOutcomes exercises the method over the real ACP
// transport. A known optionId selects; a missing or unknown one fails closed
// to "cancelled" without disturbing any existing ACP method.
func TestACPRequestPermissionOutcomes(t *testing.T) {
	tests := []struct {
		name         string
		params       map[string]any
		sendParams   bool
		wantOutcome  string
		wantOptionID string
	}{
		{
			name:         "known option is selected",
			params:       map[string]any{"sessionId": "s1", "toolCallId": "c1", "optionId": "allow_once"},
			sendParams:   true,
			wantOutcome:  "selected",
			wantOptionID: "allow_once",
		},
		{
			name:         "client-supplied options cannot widen the choice",
			params:       map[string]any{"optionId": "allow_forever", "options": []map[string]any{{"optionId": "allow_forever"}}},
			sendParams:   true,
			wantOutcome:  "cancelled",
			wantOptionID: "",
		},
		{
			name:        "no params key cancels",
			params:      nil,
			sendParams:  false,
			wantOutcome: "cancelled",
		},
		{
			name:        "empty params cancels",
			params:      map[string]any{},
			sendParams:  true,
			wantOutcome: "cancelled",
		},
		{
			name:        "unknown option cancels",
			params:      map[string]any{"sessionId": "s1", "toolCallId": "c1", "optionId": "bogus"},
			sendParams:  true,
			wantOutcome: "cancelled",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := acpPermissionRequest(t, tc.params, tc.sendParams)
			if errBody, ok := resp["error"].(map[string]any); ok {
				t.Fatalf("unexpected JSON-RPC error: %v", errBody)
			}
			result, _ := resp["result"].(map[string]any)
			outcome, _ := result["outcome"].(map[string]any)
			if outcome["outcome"] != tc.wantOutcome {
				t.Fatalf("outcome = %v, want %q (response %v)", outcome, tc.wantOutcome, resp)
			}
			gotID, hasID := outcome["optionId"]
			if tc.wantOptionID == "" {
				if hasID {
					t.Errorf("cancelled outcome must not carry optionId, got %v", gotID)
				}
				return
			}
			if gotID != tc.wantOptionID {
				t.Errorf("optionId = %v, want %q", gotID, tc.wantOptionID)
			}
		})
	}
}

// TestACPRequestPermissionMalformedParams verifies that a frame whose params
// cannot decode into the params struct is a JSON-RPC invalid-params error, not
// a successful (and possibly approving) outcome.
func TestACPRequestPermissionMalformedParams(t *testing.T) {
	resp := acpPermissionRawRequest(t,
		`{"jsonrpc":"2.0","id":1,"method":"session/request_permission","params":[]}`)
	errBody, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected a JSON-RPC error, got %v", resp)
	}
	if errBody["code"] != float64(codeInvalidParams) {
		t.Fatalf("error code = %v, want %d (response %v)", errBody["code"], codeInvalidParams, resp)
	}
}
