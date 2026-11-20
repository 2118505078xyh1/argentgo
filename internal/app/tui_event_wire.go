package app

import (
	"context"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/server"
	"github.com/2118505078xyh1/argentgo/internal/tui"
)

// tuiEventBridgeCap is the buffer in front of the TUI event loop. run.go drains
// it on every frame; a full buffer means the UI is genuinely behind, so single
// events are dropped instead of stalling the shared hub (which would evict this
// subscriber and lose the rest of the stream).
const tuiEventBridgeCap = 256

// AgentToTUIEvent converts one agent event into the frontend-facing subset the
// full-screen UI consumes. internal/tui.EventKind mirrors the two tool kinds,
// error and retry (see internal/tui/events.go); there is no text, notice, usage
// or approval kind, so those events have no TUI mapping and return false.
//
// Text deltas reach the TUI through Execute's onText callback, not this channel.
// Approval cards are created by Model.Approve (called by the app's Approver) and
// are promoted in place when the matching tool_start arrives; emitting a
// synthetic TUI event for an approval notice would mark the card running before
// the operator had answered. ParseApprovalEvent is used to recognise those
// notices explicitly so they can be skipped on purpose rather than by accident.
//
// A false result is safe to ignore: HandleEvent ignores unknown kinds anyway.
func AgentToTUIEvent(e agent.Event) (tui.Event, bool) {
	switch e.Kind {
	case agent.EventToolStart:
		return tui.Event{
			Kind:       tui.EventToolStart,
			ToolCallID: e.ToolCallID,
			ToolName:   e.ToolName,
			Args:       e.Args,
		}, true
	case agent.EventToolEnd:
		return tui.Event{
			Kind:       tui.EventToolEnd,
			ToolCallID: e.ToolCallID,
			ToolName:   e.ToolName,
			Args:       e.Args,
			Output:     e.Output,
			IsError:    e.IsError,
			DurationMS: e.DurationMS,
		}, true
	case agent.EventError:
		// EventError was added to tui.EventKind for exactly this copy: Detail
		// carries the agent's already-redacted error text.
		return tui.Event{Kind: tui.EventError, Detail: e.Err}, true
	case agent.EventRetry:
		// EventRetry is live backoff feedback. RetryInfo is wire-safe: Reason
		// is a closed decision token (never free-form provider text), and the
		// TUI redacts it again when rendering RetryNotice. A producer that
		// builds an EventRetry without the payload has nothing to show, so
		// drop it rather than fabricate an empty countdown.
		if e.Retry == nil {
			return tui.Event{}, false
		}
		return tui.Event{
			Kind:         tui.EventRetry,
			RetryReason:  e.Retry.Reason,
			RetryAttempt: e.Retry.Attempt,
			RetryMax:     e.Retry.MaxAttempts,
			RetryWaitMS:  e.Retry.DelayMS,
		}, true
	case agent.EventNotice:
		// Approval request/resolution notices ride the ordinary event stream for
		// the native and SSE transports. They have no TUI EventKind, and the
		// approval card is already rendered by Model.Approve, so skip them here.
		if kind, _, _, _, ok := agent.ParseApprovalEvent(e); ok && kind != "" {
			return tui.Event{}, false
		}
		return tui.Event{}, false
	default:
		return tui.Event{}, false
	}
}

// StartTUIEventBridge subscribes to hub and forwards mapped events into a
// buffered channel suitable for tui.Options.Events. It stops when ctx is done
// or the hub subscription ends, closing the returned channel exactly once.
// The send into out is non-blocking: a slow TUI drops events rather than
// stalling the hub read loop.
func StartTUIEventBridge(ctx context.Context, hub *server.EventHub) <-chan tui.Event {
	out := make(chan tui.Event, tuiEventBridgeCap)
	go func() {
		defer close(out)
		if hub == nil {
			return
		}
		sub, stop := hub.Subscribe(0)
		defer stop()
		for {
			select {
			case <-ctx.Done():
				return
			case ev, open := <-sub:
				if !open {
					return
				}
				tevt, ok := AgentToTUIEvent(ev)
				if !ok {
					continue
				}
				select {
				case out <- tevt:
				case <-ctx.Done():
					return
				default:
					// Slow consumer: drop this event, keep draining the hub.
				}
			}
		}
	}()
	return out
}

// TUIEventPipeline is the bundle the full-screen host needs to feed real agent
// events to the frontend: the channel for tui.Options.Events, the sink attached
// to each run context, and a cancel function that shuts both bridges down.
type TUIEventPipeline struct {
	Events <-chan tui.Event
	Emit   func(agent.Event)

	cancel context.CancelFunc
}

// NewTUIEventPipeline creates a private event hub, wires an emitter into it and
// starts the converter bridge. The TUI host calls Execute directly rather than
// going through a cli.Runner, so it has no runner EventHub to borrow; this is
// the same kind of hub app.Factory gives a runner. Each prompt attaches Emit to
// its run context with withEventEmit so Execute's Emit callback publishes here.
func NewTUIEventPipeline(ctx context.Context) *TUIEventPipeline {
	bridgeCtx, cancel := context.WithCancel(ctx)
	hub := NewEventHub()
	emit, _ := WireRunEvents(hub, bridgeCtx)
	return &TUIEventPipeline{
		Events: StartTUIEventBridge(bridgeCtx, hub),
		Emit:   emit,
		cancel: cancel,
	}
}

// Stop cancels the bridges; the Events channel closes as a result.
func (p *TUIEventPipeline) Stop() { p.cancel() }
