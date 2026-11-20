package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// ---------------------------------------------------------------------------
// Session stats: usage + roll-up persisted through the injected Recorder.
// ---------------------------------------------------------------------------

// TestStatsRecordsPersistedThroughRecorder verifies that a normal run writes
// one per-request "usage" record and one cumulative "stats" record through the
// same Recorder the app wires to Session.Append. The JSON field names must match
// session.UsageRecord / session.SessionStats so they read back after resume.
func TestStatsRecordsPersistedThroughRecorder(t *testing.T) {
	type row struct {
		kind    string
		payload any
	}
	var rows []row
	rec := RecorderFunc(func(kind string, payload any) error {
		rows = append(rows, row{kind: kind, payload: payload})
		return nil
	})

	chat := &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
		return textReply("done"), nil
	}}
	runner := &Runner{Opts: Options{
		Model:   "m",
		Chatter: chat,
		Record:  rec,
		Prices:  map[string]Price{"m": {Input: 1, Output: 2}},
	}}
	if _, err := runner.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("run: %v", err)
	}

	var usageRows, statsRows []any
	for _, r := range rows {
		switch r.kind {
		case "usage":
			usageRows = append(usageRows, r.payload)
		case "stats":
			statsRows = append(statsRows, r.payload)
		}
	}
	if len(usageRows) != 1 {
		t.Fatalf("usage records = %d, want 1", len(usageRows))
	}
	if len(statsRows) != 1 {
		t.Fatalf("stats records = %d, want 1", len(statsRows))
	}

	// Decode through JSON so the wire field names are what is being tested.
	var usage struct {
		Model  string `json:"model"`
		Input  int    `json:"input"`
		Output int    `json:"output"`
		TS     string `json:"ts"`
	}
	b, _ := json.Marshal(usageRows[0])
	if err := json.Unmarshal(b, &usage); err != nil {
		t.Fatalf("decode usage: %v", err)
	}
	if usage.Model != "m" || usage.Input <= 0 || usage.Output <= 0 || usage.TS == "" {
		t.Fatalf("usage payload = %+v, want model/positive tokens/timestamp", usage)
	}

	var stats struct {
		PromptTokens     int     `json:"prompt_tokens"`
		CompletionTokens int     `json:"completion_tokens"`
		Calls            int     `json:"calls"`
		CostUSD          float64 `json:"cost_usd"`
		CostSource       string  `json:"cost_source"`
		Updated          string  `json:"updated"`
	}
	b, _ = json.Marshal(statsRows[0])
	if err := json.Unmarshal(b, &stats); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if stats.Calls != 1 {
		t.Fatalf("stats calls = %d, want 1", stats.Calls)
	}
	if stats.PromptTokens != usage.Input || stats.CompletionTokens != usage.Output {
		t.Fatalf("stats tokens (%d/%d) != usage tokens (%d/%d)",
			stats.PromptTokens, stats.CompletionTokens, usage.Input, usage.Output)
	}
	if stats.CostUSD <= 0 || stats.CostSource != "estimated" || stats.Updated == "" {
		t.Fatalf("stats cost/source/updated = %v/%q/%q, want >0/estimated/non-empty",
			stats.CostUSD, stats.CostSource, stats.Updated)
	}
}

// TestStatsRecordsNoopWithoutRecorder verifies the feature is default-off: with
// no Recorder configured the run still works and nothing extra is persisted.
func TestStatsRecordsNoopWithoutRecorder(t *testing.T) {
	chat := &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
		return textReply("done"), nil
	}}
	runner := &Runner{Opts: Options{Model: "m", Chatter: chat}}
	res, err := runner.Run(context.Background(), "hi")
	if err != nil || res.Stopped != StopComplete {
		t.Fatalf("run: stopped=%q err=%v", res.Stopped, err)
	}
}

// ---------------------------------------------------------------------------
// Approval events: request/resolved notices bracket the Approver.Ask call.
// ---------------------------------------------------------------------------

type wireApprover struct {
	allowed bool
	err     error
	calls   int
}

func (a *wireApprover) Ask(context.Context, string, string) (bool, error) {
	a.calls++
	return a.allowed, a.err
}

func approvalEvents(events []Event) (request, resolved Event, found bool) {
	for _, e := range events {
		kind, _, _, _, ok := ParseApprovalEvent(e)
		if !ok {
			continue
		}
		switch kind {
		case "request":
			request, found = e, true
		case "resolved":
			resolved, found = e, true
		}
	}
	return request, resolved, found
}

func TestApprovalEventsBracketAsk(t *testing.T) {
	for _, tc := range []struct {
		name    string
		allowed bool
		wantOK  bool
	}{
		{"granted", true, true},
		{"denied", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			approver := &wireApprover{allowed: tc.allowed}
			var sink eventSink
			tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("write")}}
			chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
				if n == 1 {
					return toolReply(toolCall("c1", "write", `{"path":"a.txt"}`)), nil
				}
				return textReply("done"), nil
			}}
			runner := &Runner{Opts: Options{
				Model: "m", Chatter: chat, Tools: tools,
				Approve:       approver,
				MutatingTools: []string{"write"},
				Emit:          sink.emit,
			}}
			if _, err := runner.Run(context.Background(), "go"); err != nil {
				t.Fatalf("run: %v", err)
			}
			if approver.calls != 1 {
				t.Fatalf("Approver.Ask calls = %d, want 1", approver.calls)
			}

			request, resolved, found := approvalEvents(sink.events)
			if !found {
				t.Fatal("no approval events emitted")
			}
			kind, tool, id, _, ok := ParseApprovalEvent(request)
			if !ok || kind != "request" || tool != "write" || id != "c1" {
				t.Fatalf("request event = %q %q %q ok=%v", kind, tool, id, ok)
			}
			kind, tool, id, granted, ok := ParseApprovalEvent(resolved)
			if !ok || kind != "resolved" || tool != "write" || id != "c1" || granted != tc.wantOK {
				t.Fatalf("resolved event = %q %q %q granted=%v ok=%v", kind, tool, id, granted, ok)
			}

			// The digest must be a stable hash of the exact arguments.
			sum := sha256.Sum256([]byte(`{"path":"a.txt"}`))
			var payload struct {
				Digest string `json:"digest"`
			}
			prefix := approvalRequestPrefix
			if err := json.Unmarshal([]byte(request.Text[len(prefix):]), &payload); err != nil {
				t.Fatalf("decode request payload: %v", err)
			}
			if payload.Digest != hex.EncodeToString(sum[:]) {
				t.Fatalf("digest = %q, want %q", payload.Digest, hex.EncodeToString(sum[:]))
			}
		})
	}
}

// TestApprovalEventsAbsentWhenNoApprovalNeeded guards the default path: no
// approval gate means no approval events on the stream.
func TestApprovalEventsAbsentWhenNoApprovalNeeded(t *testing.T) {
	var sink eventSink
	chat := &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
		return textReply("done"), nil
	}}
	runner := &Runner{Opts: Options{Model: "m", Chatter: chat, Emit: sink.emit}}
	if _, err := runner.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, e := range sink.events {
		if kind, _, _, _, ok := ParseApprovalEvent(e); ok {
			t.Fatalf("unexpected approval event %q", kind)
		}
	}
}

// ---------------------------------------------------------------------------
// Subagent child-context seed seam (default-off).
// ---------------------------------------------------------------------------

// TestSubagentSeedContextEntersChildSystem verifies the additive SeedContext
// seam: when set, the flagged parent records reach the child's system prompt;
// when empty, the child prompt is unchanged.
func TestSubagentSeedContextEntersChildSystem(t *testing.T) {
	var childSystem string
	cfg := &SubagentConfig{
		SeedContext: []string{"user: remember the API key location"},
		NewChild: func(opts Options) ChildRunner {
			childSystem = opts.System
			return wireChildRunner{}
		},
	}
	tools := newToolset(&fakeTools{}, Options{Subagent: cfg})
	runner := &Runner{Opts: Options{
		Model: "m",
		Chatter: &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
			if n == 1 {
				return toolReply(toolCall("t1", TaskToolName, `{"prompt":"do it"}`)), nil
			}
			return textReply("done"), nil
		}},
		Tools:    tools,
		Subagent: cfg,
	}}
	if _, err := runner.Run(context.Background(), "go"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if childSystem == "" || !bytes.Contains([]byte(childSystem), []byte("remember the API key location")) {
		t.Fatalf("child system prompt did not carry seed context: %q", childSystem)
	}
}

type wireChildRunner struct{}

func (wireChildRunner) Run(context.Context, string) (Result, error) {
	return Result{Text: "child done", Stopped: StopComplete}, nil
}
