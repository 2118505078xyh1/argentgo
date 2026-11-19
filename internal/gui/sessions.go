package gui

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// tab is one chat tab: its worker, the approvals it is waiting on and the tools
// the operator allowed for the rest of the tab.
type tab struct {
	id        string
	events    <-chan Event
	approvals <-chan Approval
	commands  chan<- Command

	mu      sync.Mutex
	pending map[uint64]chan<- bool
	allowed map[string]bool

	discard chan struct{}
	once    sync.Once
}

// shutdown asks the worker to flush its session, then stops publishing its
// events. The pump keeps draining so a worker that outlives the tab never
// blocks on a full channel.
func (t *tab) shutdown() {
	t.once.Do(func() {
		select {
		case t.commands <- Command{Kind: CommandShutdown}:
		case <-time.After(2 * time.Second):
			// A wedged worker must not keep the window from closing.
		}
		close(t.discard)
	})
}

// pump forwards one tab's events to the broker until the worker or the tab
// goes away.
func (b *bridge) pump(t *tab) {
	discard := t.discard
	discarding := false
	for {
		select {
		case <-discard:
			discarding = true
			discard = nil
		case event, open := <-t.events:
			if !open {
				return
			}
			if !discarding {
				b.publish(t.id, event)
			}
		case approval, open := <-t.approvals:
			if !open {
				return
			}
			if discarding {
				reply(approval, false)
				continue
			}
			b.await(t, approval)
		}
	}
}

// await either auto-approves a tool the operator already allowed, or parks the
// request and tells the page about it.
func (b *bridge) await(t *tab, approval Approval) {
	t.mu.Lock()
	if t.allowed[approval.Name] {
		t.mu.Unlock()
		reply(approval, true)
		return
	}
	id := b.approvalSeq.Add(1)
	t.pending[id] = approval.Reply
	t.mu.Unlock()

	frame, err := json.Marshal(map[string]any{
		"kind":      "approval",
		"sid":       t.id,
		"id":        id,
		"name":      approval.Name,
		"arguments": approval.Arguments,
	})
	if err != nil {
		t.forget(id)
		reply(approval, false)
		return
	}
	b.br.broadcast(frame)
}

func (t *tab) forget(id uint64) {
	t.mu.Lock()
	delete(t.pending, id)
	t.mu.Unlock()
}

// answer delivers one decision from the page.
func (t *tab) answer(id uint64, allow bool) error {
	t.mu.Lock()
	replyTo := t.pending[id]
	delete(t.pending, id)
	t.mu.Unlock()
	if replyTo == nil {
		return errors.New("approval is no longer pending")
	}
	select {
	case replyTo <- allow:
		return nil
	default:
		return errors.New("approval receiver is gone")
	}
}

func (t *tab) allow(name string) {
	t.mu.Lock()
	t.allowed[name] = true
	t.mu.Unlock()
}

// reply pushes one decision without ever blocking the HTTP handler or the pump.
func reply(approval Approval, allow bool) {
	if approval.Reply == nil {
		return
	}
	select {
	case approval.Reply <- allow:
	default:
	}
}

// activateTab switches the tab that an empty sid resolves to.
func (b *bridge) activateTab(sid string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.tabs[sid]; !ok {
		return fmt.Errorf("session %s not found", sid)
	}
	b.active = sid
	return nil
}

// closeTab stops one worker and picks a deterministic successor.
func (b *bridge) closeTab(sid string) error {
	b.mu.Lock()
	if len(b.tabs) <= 1 {
		b.mu.Unlock()
		return errors.New("cannot close the last tab")
	}
	t, ok := b.tabs[sid]
	if !ok {
		b.mu.Unlock()
		return fmt.Errorf("session %s not found", sid)
	}
	delete(b.tabs, sid)
	delete(b.ready, sid)
	b.order = removeID(b.order, sid)
	if b.active == sid && len(b.order) > 0 {
		b.active = b.order[0]
	}
	b.mu.Unlock()
	t.shutdown()
	return nil
}

func removeID(ids []string, id string) []string {
	out := ids[:0]
	for _, value := range ids {
		if value != id {
			out = append(out, value)
		}
	}
	return out
}

// command forwards one request to a tab's worker without blocking the caller.
func (b *bridge) command(sid string, command Command) error {
	t, err := b.tab(sid)
	if err != nil {
		return err
	}
	select {
	case t.commands <- command:
		return nil
	default:
		return errors.New("agent worker is not accepting commands")
	}
}
