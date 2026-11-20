package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestScrubSessionLoadErrorFoldsAbsolutePaths checks the redaction boundary
// directly: no storage path (Windows or POSIX) may survive, while the reason and
// the caller-supplied session id must remain useful to the client.
func TestScrubSessionLoadErrorFoldsAbsolutePaths(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("home dir: %v", err)
	}
	homeSession := filepath.Join(home, ".argentgo", "sessions", "abc123.jsonl")

	cases := []struct {
		name    string
		err     error
		absent  []string
		present []string
	}{
		{
			name:    "windows drive path",
			err:     fmt.Errorf("session: read %s: access denied", `C:\Users\tester\.argentgo\sessions\abc123.jsonl`),
			absent:  []string{`C:\Users`, `C:/Users`, `tester\.argentgo`},
			present: []string{"load session \"s1\"", "access denied", "<storage>/..."},
		},
		{
			name:    "forward slash drive path",
			err:     errors.New(`session: inspect C:/Users/tester/.argentgo/sessions/abc123.jsonl: stat failed`),
			absent:  []string{`C:\Users`, `C:/Users`, "tester/.argentgo"},
			present: []string{"stat failed", "<storage>/..."},
		},
		{
			name:    "posix path",
			err:     errors.New("session: read /home/tester/.argentgo/sessions/abc123.jsonl: permission denied"),
			absent:  []string{"/home/tester", "/.argentgo"},
			present: []string{"permission denied", "<storage>/..."},
		},
		{
			name:    "real home path",
			err:     fmt.Errorf("session: hard corruption: %s: line 1: invalid JSON syntax", homeSession),
			absent:  []string{home, homeSession, filepath.ToSlash(homeSession)},
			present: []string{"line 1: invalid JSON syntax", "<storage>/..."},
		},
		{
			name:    "credential in error",
			err:     errors.New("open session failed with sk-testabcdef0123456789"),
			absent:  []string{"sk-testabcdef0123456789"},
			present: []string{"load session", "open session failed"},
		},
		{
			name:    "no path is left intact",
			err:     errors.New("session: no session with id \"missing\""),
			absent:  []string{"<storage>/..."},
			present: []string{"no session with id"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := scrubSessionLoadError("s1", tc.err)
			for _, leak := range tc.absent {
				if strings.Contains(got, leak) {
					t.Fatalf("message leaked %q: %s", leak, got)
				}
			}
			for _, keep := range tc.present {
				if !strings.Contains(got, keep) {
					t.Fatalf("message lost %q: %s", keep, got)
				}
			}
		})
	}
}

// TestRPCSessionLoadErrorDoesNotLeakStoragePath drives the real JSON-RPC method
// and asserts the wire reply never carries the absolute storage location.
func TestRPCSessionLoadErrorDoesNotLeakStoragePath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("home dir: %v", err)
	}
	storage := filepath.Join(home, ".argentgo", "sessions", "broken.jsonl")
	sessions := fakeSessions{
		err: fmt.Errorf("resume session \"broken\": session: read %s: corrupt transcript", storage),
	}

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- RunRPC(context.Background(), inR, outW, Config{Runner: &fakeRunner{}, Sessions: sessions})
	}()

	enc := json.NewEncoder(inW)
	dec := json.NewDecoder(outR)
	if err := enc.Encode(map[string]any{
		"jsonrpc": "2.0", "id": 7, "method": "session.load",
		"params": map[string]any{"id": "broken"},
	}); err != nil {
		t.Fatalf("encode request: %v", err)
	}
	response := readJSON(t, dec)
	errBody, _ := response["error"].(map[string]any)
	if errBody["code"] != float64(-32000) {
		t.Fatalf("response = %v", response)
	}
	message, _ := errBody["message"].(string)
	for _, leak := range []string{`C:\`, `C:/`, home, storage, filepath.ToSlash(storage)} {
		if strings.Contains(message, leak) {
			t.Fatalf("RPC error message leaked %q: %s", leak, message)
		}
	}
	if !strings.Contains(message, "corrupt transcript") || !strings.Contains(message, "<storage>/...") {
		t.Fatalf("RPC error message lost the reason or placeholder: %s", message)
	}

	_ = inW.Close()
	waitFor(t, done)
}
