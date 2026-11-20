package app

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/security"
	"github.com/2118505078xyh1/argentgo/internal/session"
)

// countingReader stands in for os.Stdin. It records every Read so a test can
// prove a headless approver never touches the stream the RPC server owns, and
// it keeps unread bytes intact so a stolen RPC frame would be visible too.
type countingReader struct {
	mu    sync.Mutex
	data  []byte
	reads atomic.Int32
}

func newCountingReader(body string) *countingReader {
	return &countingReader{data: []byte(body)}
}

func (r *countingReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reads.Add(1)
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

func (r *countingReader) readCount() int32 { return r.reads.Load() }

func (r *countingReader) remaining() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.data)
}

// captureApproverExecute is the runner's Execute seam: it records the approver
// the runner installed for the run and returns without a real model call.
func captureApproverExecute(ch chan<- security.Approver) func(context.Context, cli.IO, Resolved, *session.Session, string, func(string)) (agent.Result, error) {
	return func(_ context.Context, _ cli.IO, res Resolved, _ *session.Session, _ string, _ func(string)) (agent.Result, error) {
		ch <- res.Approve
		return agent.Result{}, nil
	}
}

// approvalTestRunner builds a runner with the given transport flags, drives one
// Prompt through it, and returns the runner, its pending mirror and a channel
// carrying the approver resolved for that run.
func approvalTestRunner(t *testing.T, opts cli.Options, in io.Reader) (*runner, *security.PendingStore, <-chan security.Approver) {
	t.Helper()
	env, _ := testEnv(t)
	opts.Cwd = t.TempDir()
	res, err := Resolve(cli.IO{Env: env}, opts)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	captured := make(chan security.Approver, 1)
	pending := security.NewPendingStore(t.TempDir())
	r := &runner{
		stdio:   cli.IO{In: in, Env: env},
		res:     res,
		store:   testStore(t, res),
		pending: pending,
		execute: captureApproverExecute(captured),
	}
	if _, err := r.Prompt(context.Background(), "approve test", nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	return r, pending, captured
}

// TestHeadlessApproverNeverReadsStdinAndHonoursExternalAnswer is the regression
// for the shared-stdin hazard: --rpc/--serve/--acp resolve an approver that
// must not read a byte from stdin, because server.scanLines is reading JSON-RPC
// frames from the same stream. The operator decision arrives via Resolve.
func TestHeadlessApproverNeverReadsStdinAndHonoursExternalAnswer(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts cli.Options
	}{
		{name: "rpc", opts: cli.Options{RPC: true, Approval: cli.ApprovalAsk}},
		{name: "serve", opts: cli.Options{Serve: true, Approval: cli.ApprovalAsk}},
		{name: "acp", opts: cli.Options{ACP: true, Approval: cli.ApprovalAsk}},
		{name: "rpc auto", opts: cli.Options{RPC: true, Approval: cli.ApprovalAuto}},
	} {
		for _, answer := range []struct {
			name    string
			granted bool
		}{
			{name: "allow", granted: true},
			{name: "deny", granted: false},
		} {
			t.Run(tc.name+"/"+answer.name, func(t *testing.T) {
				// Stdin holds a plausible local "y" line. A PromptApprover would
				// consume it; a headless approver must leave it untouched.
				in := newCountingReader("y\n")
				r, pending, captured := approvalTestRunner(t, tc.opts, in)
				sessionID := r.SessionID()
				approver := <-captured
				if approver == nil {
					t.Fatal("runner installed a nil approver")
				}
				if _, ok := approver.(*pendingApprover); !ok {
					t.Fatalf("headless approver type = %T, want *pendingApprover", approver)
				}

				resCh := make(chan askOutcome, 1)
				go func() {
					granted, err := approver.Ask(context.Background(), "bash", `{"command":"ls"}`)
					resCh <- askOutcome{granted: granted, err: err}
				}()

				id := waitForPendingID(t, pending, sessionID)
				// No external answer yet: stdin must be completely idle while
				// the approval is pending, and the buffered line stays intact.
				if got := in.readCount(); got != 0 {
					t.Fatalf("stdin reads while pending = %d, want 0", got)
				}
				if got := in.remaining(); got != "y\n" {
					t.Fatalf("stdin after pending = %q, want %q (an RPC frame would be stolen)", got, "y\n")
				}

				if err := pending.Resolve(sessionID, id, answer.granted); err != nil {
					t.Fatalf("Resolve: %v", err)
				}
				select {
				case got := <-resCh:
					if got.err != nil || got.granted != answer.granted {
						t.Fatalf("Ask = %v, %v; want %v, nil", got.granted, got.err, answer.granted)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("headless Ask did not unblock on the external answer")
				}
				if got := in.readCount(); got != 0 {
					t.Fatalf("stdin reads after answer = %d, want 0", got)
				}
				if got := in.remaining(); got != "y\n" {
					t.Fatalf("stdin after answer = %q, want %q", got, "y\n")
				}
				if items, err := pending.List(sessionID); err != nil || len(items) != 0 {
					t.Fatalf("pending after answer = %v, %v; want empty", items, err)
				}
			})
		}
	}
}

// TestHeadlessApproverCancellationUnblocksWithoutReadingStdin covers the case
// where no operator ever answers: the run context must release Ask, and stdin
// still must not be touched.
func TestHeadlessApproverCancellationUnblocksWithoutReadingStdin(t *testing.T) {
	in := newCountingReader("y\n")
	r, pending, captured := approvalTestRunner(t, cli.Options{RPC: true}, in)
	sessionID := r.SessionID()
	approver := <-captured

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resCh := make(chan askOutcome, 1)
	go func() {
		granted, err := approver.Ask(ctx, "bash", `{"command":"sleep 1"}`)
		resCh <- askOutcome{granted: granted, err: err}
	}()
	waitForPendingID(t, pending, sessionID)
	cancel()
	select {
	case got := <-resCh:
		if got.err == nil || got.granted {
			t.Fatalf("cancelled Ask = %v, %v; want false, non-nil error", got.granted, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("headless Ask did not unblock on ctx cancellation")
	}
	if got := in.readCount(); got != 0 {
		t.Fatalf("stdin reads after cancellation = %d, want 0", got)
	}
}

// TestCLIDirectApproverStillReadsLocalYn proves the non-headless path is byte-
// for-byte unchanged: a direct CLI run still asks y/n on the operator's tty.
func TestCLIDirectApproverStillReadsLocalYn(t *testing.T) {
	for _, tc := range []struct {
		name    string
		input   string
		granted bool
	}{
		{name: "yes", input: "y\n", granted: true},
		{name: "lowercase word", input: "yes\n", granted: true},
		{name: "no", input: "n\n", granted: false},
		{name: "empty line", input: "\n", granted: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := newCountingReader(tc.input)
			r, pending, captured := approvalTestRunner(t, cli.Options{}, in)
			approver := <-captured
			granted, err := approver.Ask(context.Background(), "bash", `{"command":"pwd"}`)
			if err != nil || granted != tc.granted {
				t.Fatalf("Ask = %v, %v; want %v, nil", granted, err, tc.granted)
			}
			if got := in.readCount(); got == 0 {
				t.Fatal("direct CLI approver never read stdin")
			}
			if items, err := pending.List(r.SessionID()); err != nil || len(items) != 0 {
				t.Fatalf("pending after local answer = %v, %v; want empty", items, err)
			}
		})
	}
}

// TestHeadlessApproverNilStoreDenies pins the null-object behaviour: with no
// pending mirror there is no answer channel, so the headless approver denies
// instead of blocking on stdin or waiting forever.
func TestHeadlessApproverNilStoreDenies(t *testing.T) {
	approver := WithHeadlessPendingApprovals(nil, "s")
	if _, ok := approver.(security.NeverApprover); !ok {
		t.Fatalf("nil-store headless approver = %T, want security.NeverApprover", approver)
	}
	granted, err := approver.Ask(context.Background(), "bash", `{}`)
	if err != nil || granted {
		t.Fatalf("Ask = %v, %v; want false, nil", granted, err)
	}
}
