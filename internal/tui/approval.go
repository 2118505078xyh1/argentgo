package tui

import (
	"context"
)

// approvalRequest is one tool call waiting for the operator.
type approvalRequest struct {
	name  string
	args  string
	reply chan bool
	ctx   context.Context
}

func (m *Model) answerApprovalLocked(key Key) KeyAction {
	request := m.pending
	if request == nil {
		return ActionNone
	}
	switch key.Kind {
	case KindRune:
		switch key.Rune {
		case 'y', 'Y':
			m.finishApprovalLocked(true, text(m.locale, "approved"))
		case 'n', 'N':
			m.finishApprovalLocked(false, text(m.locale, "denied"))
		case 'a', 'A':
			m.allowed[request.name] = true
			m.finishApprovalLocked(true, text(m.locale, "approved_session"))
		}
	case KindEsc, KindCtrlC:
		m.finishApprovalLocked(false, text(m.locale, "denied"))
	}
	return ActionNone
}

func (m *Model) finishApprovalLocked(ok bool, status string) {
	request := m.pending
	if request == nil {
		return
	}
	m.pending = nil
	select {
	case request.reply <- ok:
	default:
	}
	m.setStatusLocked(status)
	m.promoteApprovalLocked()
}

func (m *Model) promoteApprovalLocked() {
	for m.pending == nil && len(m.approvalQueue) > 0 {
		next := m.approvalQueue[0]
		m.approvalQueue = m.approvalQueue[1:]
		if next.ctx != nil && next.ctx.Err() != nil {
			continue
		}
		m.pending = &next
	}
}

func (m *Model) dropApprovalLocked(reply chan bool) {
	if m.pending != nil && m.pending.reply == reply {
		m.pending = nil
		m.promoteApprovalLocked()
		return
	}
	for i := range m.approvalQueue {
		if m.approvalQueue[i].reply == reply {
			m.approvalQueue = append(m.approvalQueue[:i], m.approvalQueue[i+1:]...)
			return
		}
	}
}

// Approve asks the operator to allow a tool call and blocks until the modal is
// answered or ctx ends. The answer is returned through the injected
// Options.Approval authority when one is installed.
func (m *Model) Approve(ctx context.Context, name, args string) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	m.mu.Lock()
	if m.allowed[name] {
		m.mu.Unlock()
		return true, nil
	}
	request := approvalRequest{name: name, args: args, reply: make(chan bool, 1), ctx: ctx}
	if m.pending == nil {
		m.pending = &request
	} else {
		m.approvalQueue = append(m.approvalQueue, request)
	}
	m.mu.Unlock()

	var ok bool
	select {
	case ok = <-request.reply:
	case <-ctx.Done():
		m.mu.Lock()
		m.dropApprovalLocked(request.reply)
		m.mu.Unlock()
		return false, ctx.Err()
	}
	if !ok {
		return false, nil
	}
	m.mu.Lock()
	authority := m.approvalFn
	m.mu.Unlock()
	if authority != nil {
		return authority(ctx, name, args)
	}
	return true, nil
}

// PendingApproval reports the tool call the modal is showing.
func (m *Model) PendingApproval() (name, args string, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending == nil {
		return "", "", false
	}
	return m.pending.name, m.pending.args, true
}

// Tick drops an approval whose context is gone; the loop calls it each frame.
func (m *Model) Tick() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending != nil && m.pending.ctx != nil && m.pending.ctx.Err() != nil {
		m.pending = nil
		m.promoteApprovalLocked()
	}
}
