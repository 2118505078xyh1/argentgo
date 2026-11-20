package tui

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"
)

// approvalGrantKey scopes a session grant to one tool plus its detail. The NUL
// separator keeps name/detail pairs from colliding with bare name keys.
func approvalGrantKey(name, args string) string {
	return name + "\x00" + approvalDetail(args)
}

// approvalDetail extracts the argument that identifies a call. JSON calls use
// the first path/command field (cleaned), so granting `bash` with `ls` cannot
// silently grant `bash` with `sudo`. Plain-string args, used by older callers,
// fall back to the first token; that keeps "ls" and "ls -la" on one grant,
// matching the historical coarse behaviour for those inputs.
func approvalDetail(args string) string {
	trimmed := strings.TrimSpace(args)
	if strings.HasPrefix(trimmed, "{") {
		var decoded map[string]any
		if err := json.Unmarshal([]byte(trimmed), &decoded); err == nil {
			for _, key := range []string{"command", "cmd", "path", "file", "filepath", "query", "pattern", "url", "uri", "name", "job"} {
				value, ok := decoded[key].(string)
				if !ok || strings.TrimSpace(value) == "" {
					continue
				}
				switch key {
				case "path", "file", "filepath":
					return filepath.Clean(strings.TrimSpace(value))
				default:
					return collapseSpace(value)
				}
			}
		}
	}
	if fields := strings.Fields(trimmed); len(fields) > 0 {
		return fields[0]
	}
	return ""
}

// approvalRequest is one tool call waiting for the operator.
type approvalRequest struct {
	name string
	args string
	// danger is a human-readable reason the call was flagged as dangerous.
	// Empty (the default) keeps the historical modal byte-for-byte unchanged.
	// When set, the modal shows the reason and pre-selects Deny as the safe
	// default so a habitual Enter cannot approve a destructive command.
	danger string
	reply  chan bool
	ctx    context.Context
	// openedAt 记录对话框真正显示给操作者的时刻，供可选的按键宽限期判断。
	// 排队等待的请求在被提升为当前对话框时会重新打戳，因此这里衡量的是
	// “弹窗出现”而非“请求创建”的时间。零值与关闭宽限期时完全等价。
	openedAt time.Time
}

// defaultsToDeny reports whether this request pre-selects Deny. Ordinary
// requests keep Allow as the primary affordance and are deliberately left
// untouched; only flagged requests flip the safe default.
func (r *approvalRequest) defaultsToDeny() bool {
	return r != nil && r.danger != ""
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
			// Grant only this exact call: a coarse name key would allow every
			// later invocation of the same tool with different arguments.
			m.allowed[approvalGrantKey(request.name, request.args)] = true
			m.finishApprovalLocked(true, text(m.locale, "approved_session"))
		}
	case KindEsc, KindCtrlC:
		m.finishApprovalLocked(false, text(m.locale, "denied"))
	case KindEnter:
		// A flagged request pre-selects Deny: an ambiguous confirm key must
		// never approve a destructive command. Non-danger requests keep their
		// historical no-op so their behaviour is unchanged.
		if request.defaultsToDeny() {
			m.finishApprovalLocked(false, text(m.locale, "denied"))
		}
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
		// 排队请求此刻才真正出现在屏幕上，重新打戳让宽限期从弹窗时刻算起。
		next.openedAt = m.now()
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
// Options.Approval authority when one is installed. It is the ordinary,
// non-dangerous entry point: the modal keeps its historical Allow default.
func (m *Model) Approve(ctx context.Context, name, args string) (bool, error) {
	return m.approve(ctx, name, args, "")
}

// ApproveDanger is the dangerous-command entry point. A non-empty danger reason
// is shown in the modal and flips the default answer to Deny. The app layer
// (which owns security.FindFileEditingCommand) is responsible for supplying it;
// the TUI package does not import security. With an empty danger it behaves
// exactly like Approve.
func (m *Model) ApproveDanger(ctx context.Context, name, args, danger string) (bool, error) {
	return m.approve(ctx, name, args, danger)
}

func (m *Model) approve(ctx context.Context, name, args, danger string) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	m.mu.Lock()
	// The bare name key is still honoured so grants created by older callers
	// (which cached m.allowed directly) keep working. New grants are composite.
	if m.allowed[name] || m.allowed[approvalGrantKey(name, args)] {
		m.mu.Unlock()
		return true, nil
	}
	request := approvalRequest{name: name, args: args, danger: danger, reply: make(chan bool, 1), ctx: ctx}
	m.appendTaskLocked(Task{Name: name, Args: args, Status: TaskPending})
	if m.pending == nil {
		request.openedAt = m.now()
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
