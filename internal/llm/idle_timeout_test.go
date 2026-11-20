package llm_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// TestChatIdleTimeoutSurfacedAsStreamError checks the public path: the field is
// wired through Chat and a dead-but-open connection becomes a stream_error
// attempt (retryable class) rather than a hang until the context expires.
func TestChatIdleTimeoutSurfacedAsStreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done() // stay connected but silent
	}))
	defer server.Close()

	var records []llm.AttemptRecord
	client := llm.New(server.URL, "")
	client.IdleTimeout = 80 * time.Millisecond
	client.Options.AttemptHook = func(rec llm.AttemptRecord) { records = append(records, rec) }

	_, err := client.Chat(context.Background(), "m", nil, nil, nil)
	if !errors.Is(err, llm.ErrStreamError) {
		t.Fatalf("err = %v, want ErrStreamError", err)
	}
	if len(records) != 1 || records[0].Kind != llm.AttemptStreamError {
		t.Fatalf("attempt records = %+v, want one stream_error", records)
	}
	if !strings.Contains(records[0].ErrorClass, "idle") {
		t.Fatalf("error class = %q, want the idle reason", records[0].ErrorClass)
	}
}
