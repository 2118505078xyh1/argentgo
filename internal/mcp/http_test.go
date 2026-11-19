package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// legacySSEServer is an MCP 2024-11-05 server: GET /sse announces the POST
// endpoint, POST /message?sessionId=... accepts requests, answers on the stream.
func legacySSEServer(t *testing.T) (*httptest.Server, chan string) {
	t.Helper()
	outbound := make(chan string, 32)

	mux := http.NewServeMux()
	mux.HandleFunc("/sse", func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		fmt.Fprint(w, "event: endpoint\ndata: /message?sessionId=abc\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		for {
			select {
			case payload := <-outbound:
				fmt.Fprintf(w, "event: message\ndata: %s\n\n", payload)
				if flusher != nil {
					flusher.Flush()
				}
			case <-r.Context().Done():
				return
			}
		}
	})
	mux.HandleFunc("/message", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("sessionId") != "abc" {
			http.Error(w, "wrong session id", http.StatusBadRequest)
			return
		}
		var message map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&message); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		idRaw, hasID := message["id"]
		if !hasID {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var method string
		_ = json.Unmarshal(message["method"], &method)
		result, callErr := helperReply(method, message["params"])
		payload := map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(idRaw)}
		if callErr != nil {
			payload["error"] = map[string]any{"code": -32602, "message": callErr.Error()}
		} else {
			payload["result"] = result
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		select {
		case outbound <- string(encoded):
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusAccepted)
	})

	return httptest.NewServer(mux), outbound
}

func TestHTTPTransportLegacySSE(t *testing.T) {
	server, _ := legacySSEServer(t)
	defer server.Close()

	c, err := Connect(context.Background(), Spec{
		Name:      "legacy",
		Transport: "http",
		URL:       server.URL + "/sse",
		Timeout:   5 * time.Second,
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer c.Close()

	info, err := c.Initialize(context.Background())
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if info.Name != "fake" {
		t.Fatalf("unexpected server info: %+v", info)
	}

	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(tools) != 2 || tools[0].Name != "echo" {
		t.Fatalf("unexpected tools: %+v", tools)
	}

	text, err := c.CallTool(context.Background(), "echo", json.RawMessage(`{"text":"hi"}`))
	if err != nil || text != "echo: hi" {
		t.Fatalf("call echo: text=%q err=%v", text, err)
	}

	text, err = c.CallTool(context.Background(), "boom", nil)
	if err == nil || text != "kaboom" {
		t.Fatalf("isError not surfaced: text=%q err=%v", text, err)
	}
}

func TestHTTPTransportStreamable(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			// No event stream here: the client must fall back to POST only.
			http.Error(w, "streaming not supported", http.StatusMethodNotAllowed)
			return
		}
		var message map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&message); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		idRaw, hasID := message["id"]
		if !hasID {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var method string
		_ = json.Unmarshal(message["method"], &method)
		result, callErr := helperReply(method, message["params"])
		payload := map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(idRaw)}
		if callErr != nil {
			payload["error"] = map[string]any{"code": -32602, "message": callErr.Error()}
		} else {
			payload["result"] = result
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	c, err := Connect(context.Background(), Spec{
		Name:      "streamable",
		Transport: "http",
		URL:       server.URL + "/mcp",
		Timeout:   5 * time.Second,
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer c.Close()

	if _, err := c.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("unexpected tools: %+v", tools)
	}
	text, err := c.CallTool(context.Background(), "echo", json.RawMessage(`{"text":"hi"}`))
	if err != nil || text != "echo: hi" {
		t.Fatalf("call echo: text=%q err=%v", text, err)
	}
}

func TestHTTPTransportReportsServerErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			http.Error(w, "no stream", http.StatusMethodNotAllowed)
			return
		}
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	c, err := Connect(context.Background(), Spec{
		Name:      "broken",
		Transport: "http",
		URL:       server.URL + "/mcp",
		Timeout:   2 * time.Second,
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer c.Close()

	_, err = c.Initialize(context.Background())
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("status not surfaced: %v", err)
	}
}

func TestResolveEndpoint(t *testing.T) {
	cases := map[string]string{
		"http://127.0.0.1:9000/sse|/message?sessionId=1":   "http://127.0.0.1:9000/message?sessionId=1",
		"http://127.0.0.1:9000/sse|message?sessionId=1":    "http://127.0.0.1:9000/message?sessionId=1",
		"http://127.0.0.1:9000/sse|http://other.example/m": "http://other.example/m",
	}
	for input, want := range cases {
		parts := strings.SplitN(input, "|", 2)
		if got := resolveEndpoint(parts[0], parts[1]); got != want {
			t.Fatalf("resolveEndpoint(%q, %q) = %q, want %q", parts[0], parts[1], got, want)
		}
	}
}
