package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/security"
	"github.com/2118505078xyh1/argentgo/internal/server"
	"github.com/2118505078xyh1/argentgo/internal/session"
)

// eventBridgeCap bounds the per-run channel between the agent loop and the hub
// bridge. server.WireAgentEvents drains it promptly, so EmitEvent blocks only
// momentarily and no event is dropped.
const eventBridgeCap = 1024

// nextPendingID gives durable approval records a collision-free correlation id
// without needing a tool-call id (security.Approver.Ask does not receive one).
var nextPendingID atomic.Uint64

// eventEmitKey carries the per-run event sink on the run context. The agent
// loop's Emit callback reads it with EmitEvent, so the Execute signature does
// not have to change and callers that never bridge events stay unaffected.
type eventEmitKey struct{}

// NewEventHub returns the hub the headless transports publish to. It is
// exposed so an integrator can set server.Config.Events explicitly when it
// builds a transport outside app.Factory.
func NewEventHub() *server.EventHub { return server.NewEventHub() }

// WireRunEvents starts the bridge that forwards agent events into hub. Call the
// returned emit function from the agent loop's Emit/OnEvent callback for every
// event. The bridge stops when ctx is cancelled or emit is closed; done closes
// when its goroutine exits. A nil hub disables bridging: emit is a no-op and
// done is already closed, so callers can pass it unconditionally.
func WireRunEvents(hub *server.EventHub, ctx context.Context) (emit func(agent.Event), done <-chan struct{}) {
	if hub == nil {
		closed := make(chan struct{})
		close(closed)
		return func(agent.Event) {}, closed
	}
	events := make(chan agent.Event, eventBridgeCap)
	done = server.WireAgentEvents(hub, events, ctx)
	emit = func(ev agent.Event) {
		select {
		case events <- ev:
		case <-ctx.Done():
			// The bridge is shutting down; dropping this final event is safe and
			// keeps a cancelled run from blocking on a full buffer.
		}
	}
	return emit, done
}

func withEventEmit(ctx context.Context, emit func(agent.Event)) context.Context {
	return context.WithValue(ctx, eventEmitKey{}, emit)
}

// EmitEvent publishes ev to the event bridge attached to ctx, if any. The agent
// loop's Emit callback calls this so --serve and --rpc subscribers observe the
// same events as the in-process caller. With no bridge it is a no-op.
func EmitEvent(ctx context.Context, ev agent.Event) {
	if emit, ok := ctx.Value(eventEmitKey{}).(func(agent.Event)); ok && emit != nil {
		emit(ev)
	}
}

// EventHubOf reports the hub attached to a runner created by Factory. The
// headless transports pick it up automatically through Runner's EventHub
// method, but exposing it here lets an integrator set server.Config.Events
// explicitly. It returns nil for foreign/fake runners, preserving the
// "no hub, no stream" default.
func EventHubOf(r cli.Runner) *server.EventHub {
	if typed, ok := r.(interface{ EventHub() *server.EventHub }); ok {
		return typed.EventHub()
	}
	return nil
}

// WithPendingApprovals mirrors approver decisions into a durable PendingStore.
// The decorator is transparent: the operator's answer is returned unchanged,
// while a pending record exists on disk from the moment the question is asked
// until it is answered. A nil store or inner approver disables mirroring.
func WithPendingApprovals(store *security.PendingStore, session string, inner security.Approver) security.Approver {
	if store == nil || inner == nil {
		return inner
	}
	return &pendingApprover{store: store, session: session, inner: inner}
}

type pendingApprover struct {
	store   *security.PendingStore
	session string
	inner   security.Approver
}

func (p *pendingApprover) Ask(ctx context.Context, name, args string) (bool, error) {
	id := fmt.Sprintf("%s-%d", name, nextPendingID.Add(1))
	// Best-effort durability: a failed write must never block or change the
	// approval decision, which stays a purely in-memory interaction.
	_ = p.store.Add(security.PendingApproval{
		ID:         id,
		Session:    p.session,
		Tool:       name,
		ArgsDigest: security.PendingArgsDigest(args),
	})
	granted, err := p.inner.Ask(ctx, name, args)
	_ = p.store.Resolve(p.session, id, granted)
	return granted, err
}

// Factory returns the runner factory the CLI expects.
func Factory(stdio cli.IO) cli.RunnerFactory {
	return func(opts cli.Options) (cli.Runner, error) {
		resolved, err := Resolve(stdio, opts)
		if err != nil {
			return nil, err
		}
		store, err := session.Open(filepath.Join(resolved.Storage, "sessions"))
		if err != nil {
			return nil, fmt.Errorf("open sessions: %w", err)
		}
		// One hub and one pending store per serve/runner instance. Transports
		// discover the hub through Runner.EventHub, and each run wraps its
		// approver so pending requests are durable across reconnects.
		return &runner{
			stdio:   stdio,
			res:     resolved,
			store:   store,
			execute: Execute,
			hub:     NewEventHub(),
			pending: security.NewPendingStore(filepath.Join(resolved.Storage, "pending")),
		}, nil
	}
}

// ActiveSessionID reports the session a runner is appending to, when it
// exposes one. Callers holding only a cli.Runner can use this to show or log
// the resumed session id.
func ActiveSessionID(r cli.Runner) string {
	if typed, ok := r.(interface{ SessionID() string }); ok {
		return typed.SessionID()
	}
	return ""
}

type runner struct {
	stdio cli.IO
	res   Resolved
	store *session.Store

	// hub is shared by every run on this instance and discovered by the
	// headless transports through EventHub(). pending mirrors approvals to disk.
	// Both are nil on hand-built test runners, keeping their behaviour unchanged.
	hub     *server.EventHub
	pending *security.PendingStore

	// execute and claimHook are per-instance seams for Prompt. Production sets
	// execute to Execute (see Factory) and leaves claimHook nil; tests inject
	// fakes on their own runner instead of replacing package globals, so they
	// can run in parallel.
	execute   func(context.Context, cli.IO, Resolved, *session.Session, string, func(string)) (agent.Result, error)
	claimHook func(*runner)

	// All of busy, cancel and current are protected by mu. In particular the
	// single-flight slot (busy) and its cancel func are installed and cleared
	// in the same critical section, so Abort can never observe "busy but no
	// cancel registered" and lose the abort.
	mu      sync.Mutex
	busy    bool
	cancel  context.CancelFunc
	current *session.Session
}

// Prompt satisfies cli.Runner. At most one prompt may be in flight on a runner.
func (r *runner) Prompt(ctx context.Context, text string, onText func(string)) (string, error) {
	ctx, cancel := context.WithCancel(ctx)

	// Bridge this run's agent events into the shared hub. The bridge reads from
	// a per-run channel and stops with the run context, so no goroutine or stale
	// event outlives the prompt. EmitEvent is a no-op unless the run's Emit
	// callback forwards events through ctx (see the wiring note in the report).
	if r.hub != nil {
		emit, _ := WireRunEvents(r.hub, ctx)
		ctx = withEventEmit(ctx, emit)
	}

	// Claim the single-flight slot and register the cancel func under one
	// critical section. Abort uses the same mu, so it either sees no prompt at
	// all (no-op) or sees a fully registered cancel func and can cancel it.
	r.mu.Lock()
	if r.busy {
		r.mu.Unlock()
		cancel() // do not leak the derived context when the prompt is refused
		return "", errors.New("prompt already running")
	}
	r.busy = true
	r.cancel = cancel
	r.mu.Unlock()

	// Test seam: by this point busy and cancel are both installed. Production
	// leaves the hook nil.
	if r.claimHook != nil {
		r.claimHook(r)
	}

	// Clear the cancel slot and release the slot atomically under the same mu,
	// mutually exclusive with Prompt and Abort.
	defer func() {
		r.mu.Lock()
		r.cancel = nil
		r.busy = false
		r.mu.Unlock()
		cancel()
	}()

	sess, err := r.ensureSession(text)
	if err != nil {
		return "", err
	}
	// Mirror approvals to the durable store for this session. A per-run copy of
	// Resolved keeps the wrapper out of r.res; hand-built test runners (pending
	// == nil) see the original Resolved and unchanged behaviour.
	execRes := r.res
	if r.pending != nil {
		_ = r.pending.Clear(sess.ID()) // a new run supersedes stale requests
		approve := execRes.Approve
		if approve == nil {
			approve = approverFor(r.stdio, execRes)
		}
		execRes.Approve = WithPendingApprovals(r.pending, sess.ID(), approve)
	}
	result, err := r.execute(ctx, r.stdio, execRes, sess, text, onText)
	return result.Text, err
}

// Abort satisfies cli.Runner.
func (r *runner) Abort() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil {
		r.cancel()
	}
}

// EventHub exposes the shared hub to the headless transports. The exported
// method lets server.NewHandler/RunRPC discover it through a small interface,
// without internal/server importing internal/app.
func (r *runner) EventHub() *server.EventHub {
	return r.hub
}

// SessionID reports the session the runner is currently appending to. It is
// empty before the first prompt.
func (r *runner) SessionID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current == nil {
		return ""
	}
	return r.current.ID()
}

func (r *runner) ensureSession(firstPrompt string) (*session.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current != nil {
		return r.current, nil
	}
	sess, err := openOrCreateSession(r.store, r.res.Options, firstPrompt)
	if err != nil {
		return nil, err
	}
	r.current = sess
	return sess, nil
}
