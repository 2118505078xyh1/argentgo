package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// credentialBearingError models a provider failure whose body echoes the
// server's own secrets. Provider error bodies are returned verbatim by the
// agent/llm layers; none of these spans may reach a transport client, which
// only holds the bearer token and must never learn the configured API key.
func credentialBearingError() error {
	return errors.New(
		"provider returned 401 Unauthorized: api_key=sk-testabcdef0123456789 " +
			"password=hunter2secret99 user@example.com " +
			"Authorization: Bearer sk-anotherkey0123456789",
	)
}

var credentialLeaks = []string{
	"sk-testabcdef0123456789",
	"hunter2secret99",
	"user@example.com",
	"sk-anotherkey0123456789",
}

func assertNoCredentialLeak(t *testing.T, wire string) {
	t.Helper()
	for _, secret := range credentialLeaks {
		if strings.Contains(wire, secret) {
			t.Fatalf("wire reply leaked %q: %s", secret, wire)
		}
	}
	if !strings.Contains(wire, "401") {
		t.Fatalf("wire reply lost the canonical reason: %s", wire)
	}
}

func TestWireErrorTextRedactsCredentials(t *testing.T) {
	got := wireErrorText(credentialBearingError())
	assertNoCredentialLeak(t, got)
	if !strings.Contains(got, "provider returned 401 Unauthorized") {
		t.Fatalf("wireErrorText = %q, want the error wording preserved", got)
	}
}

// TestHTTPPromptFailureRedactsCredentials covers both HTTP failure paths: a
// failure before any text (JSON error body with a non-2xx status) and one after
// the stream is committed (terminal SSE error event). Both used to carry the
// raw provider error.
func TestHTTPPromptFailureRedactsCredentials(t *testing.T) {
	t.Run("pre-stream JSON error", func(t *testing.T) {
		h := NewHandler(Config{Token: "t", Runner: &failingRunner{err: credentialBearingError()}})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/prompt", strings.NewReader(`{"text":"hi"}`))
		req.Header.Set("Authorization", "Bearer t")
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401 (auth family)", rec.Code)
		}
		assertNoCredentialLeak(t, rec.Body.String())
	})

	t.Run("post-stream SSE error", func(t *testing.T) {
		h := NewHandler(Config{Token: "t", Runner: &cancellingRunner{err: credentialBearingError()}})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/prompt", strings.NewReader(`{"text":"hi"}`))
		req.Header.Set("Authorization", "Bearer t")
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (header already committed)", rec.Code)
		}
		events := decodeEvents(t, rec.Body.String())
		if len(events) != 2 || events[1]["type"] != "error" {
			t.Fatalf("events = %v, want text then error", events)
		}
		errText, _ := events[1]["error"].(string)
		assertNoCredentialLeak(t, errText)
	})
}

// TestRPCPromptFailureRedactsCredentials drives the real --rpc prompt method
// and checks the -32603 error message crossing the wire.
func TestRPCPromptFailureRedactsCredentials(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- RunRPC(context.Background(), inR, outW, Config{
			Runner: &failingRunner{err: credentialBearingError()},
		})
	}()

	if err := json.NewEncoder(inW).Encode(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "prompt",
		"params": map[string]any{"text": "hi"},
	}); err != nil {
		t.Fatalf("encode prompt: %v", err)
	}
	resp := readJSON(t, json.NewDecoder(outR))
	body, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected a JSON-RPC error, got %v", resp)
	}
	msg, _ := body["message"].(string)
	assertNoCredentialLeak(t, msg)

	_ = inW.Close()
	waitFor(t, done)
}

// TestACPPromptFailureRedactsCredentials drives the real ACP prompt method and
// checks the -32603 error message crossing the wire.
func TestACPPromptFailureRedactsCredentials(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- RunACP(context.Background(), inR, outW, Config{
			Runner: &failingRunner{err: credentialBearingError()},
		})
	}()

	enc := json.NewEncoder(inW)
	dec := json.NewDecoder(outR)

	// Create a session first: ACP rejects prompts against unknown session ids
	// before the runner is ever called, and that validation error is not the
	// provider failure under test.
	_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "session/new"})
	created := readJSON(t, dec)
	createdBody, _ := created["result"].(map[string]any)
	sid, _ := createdBody["sessionId"].(string)

	_ = enc.Encode(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "session/prompt",
		"params": map[string]any{"sessionId": sid, "prompt": "hi"},
	})
	var promptResult map[string]any
	for {
		msg := readJSON(t, dec)
		if msg["id"] == float64(2) {
			promptResult = msg
			break
		}
		if msg["method"] != "session/update" {
			t.Fatalf("unexpected message = %v", msg)
		}
	}
	body, ok := promptResult["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected a JSON-RPC error, got %v", promptResult)
	}
	msg, _ := body["message"].(string)
	assertNoCredentialLeak(t, msg)

	_ = inW.Close()
	waitFor(t, done)
}
