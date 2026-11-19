package agent_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argent-go/internal/agent"
	"github.com/2118505078xyh1/argent-go/internal/llm"
	"github.com/2118505078xyh1/argent-go/internal/tools"
)

func TestLoopRunsToolThenAnswers(t *testing.T) {
	root := t.TempDir()
	requests := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		if requests == 1 {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"function\":{\"name\":\"write\",\"arguments\":\"{\\\"path\\\":\\\"out.txt\\\",\\\"content\\\":\\\"hello\\\"}\"}}]}}]}\n\n")
		} else {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"}}]}\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	runner := &agent.Runner{
		Client: llm.New(server.URL, ""),
		Tools:  tools.New(root, true),
		Opts:   agent.Options{Model: "test", MaxTurns: 5, Root: root},
	}

	result, err := runner.Run(context.Background(), "write hello")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result != "done" {
		t.Fatalf("result = %q, want %q", result, "done")
	}
	data, err := os.ReadFile(filepath.Join(root, "out.txt"))
	if err != nil {
		t.Fatalf("tool did not write the file: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("file content = %q, want %q", data, "hello")
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
}

func TestLoopStopsAtTurnLimit(t *testing.T) {
	root := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"function\":{\"name\":\"ls\",\"arguments\":\"{}\"}}]}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	runner := &agent.Runner{
		Client: llm.New(server.URL, ""),
		Tools:  tools.New(root, true),
		Opts:   agent.Options{Model: "test", MaxTurns: 2, Root: root},
	}
	_, err := runner.Run(context.Background(), "loop forever")
	if err == nil || !strings.Contains(err.Error(), "turn limit") {
		t.Fatalf("err = %v, want turn limit", err)
	}
}
