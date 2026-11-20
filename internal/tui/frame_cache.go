package tui

import "time"

// frameCacheKey is a snapshot of every input that can change the cell grid Paint
// writes into a Buffer. Two frames with equal keys are guaranteed to paint the
// same cells; two frames with different keys require a fresh Paint.
//
// This is a *capability* only: nothing in the run loop consumes it yet. It is
// deliberately conservative — when time-sensitive content is on screen the key
// carries a raw clock token so no two instants can compare equal, which forbids
// reuse rather than risk a stale frame. See the report in the change description
// for why draw is not wired to this key.
type frameCacheKey struct {
	width, height int
	version       int // content version: entries, editor, theme, sessions, plan, ...
	scroll        int
	themeEpoch    int
	noColor       bool

	// clockSecond tracks the header clock (1-second resolution).
	clockSecond int
	// timeSensitive is true when a notice, spinner, live reasoning/tool row or
	// legacy status is visible. In that case nowNanos pins the exact instant so
	// the key never matches a later frame. When false, nowNanos is zero.
	timeSensitive bool
	nowNanos      int64

	usageIn, usageOut, usageCached int
	contextWindow                  int
	cost                           string

	selAnchor, selActive BufferPos
	pending              bool
	pendingName          string
	pendingArgs          string

	// Panel geometry. Panel *data* changes bump version; these scalar fields
	// cover the cursor/scroll/filter moves that only mark the model dirty.
	showTasks, showWorkspace, showPlan, showSessions bool
	tasksScroll, workspaceCursor, workspaceScroll    int
	previewScroll, planScroll                        int
	sessionsCursor, sessionsScroll                   int
	sessionFilter                                    string
	sessionFiltering                                 bool
	expanded                                         bool
}

// newFrameCacheKey snapshots the model under its own lock. It never mutates the
// model: notice presence is read with Len rather than Current, which prunes
// expired notices (and would therefore alter the stack) as a side effect. An
// expired-but-not-yet-pruned notice only makes the key more conservative.
func newFrameCacheKey(m *Model, width, height int) frameCacheKey {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.now()
	k := frameCacheKey{
		width:            width,
		height:           height,
		version:          m.version,
		scroll:           m.scroll,
		themeEpoch:       m.themeEpoch,
		noColor:          m.noColor,
		clockSecond:      int(now.Sub(m.started).Seconds()),
		usageIn:          m.usageIn,
		usageOut:         m.usageOut,
		usageCached:      m.usageCached,
		contextWindow:    m.contextWindow,
		cost:             m.cost,
		selAnchor:        m.selAnchor,
		selActive:        m.selActive,
		showTasks:        m.showTasks,
		showWorkspace:    m.showWorkspace,
		showPlan:         m.showPlan,
		showSessions:     m.showSessions,
		tasksScroll:      m.tasksScroll,
		workspaceCursor:  m.workspaceCursor,
		workspaceScroll:  m.workspaceScroll,
		previewScroll:    m.previewScroll,
		planScroll:       m.planScroll,
		sessionsCursor:   m.sessionsCursor,
		sessionsScroll:   m.sessionsScroll,
		sessionFilter:    m.sessionFilter,
		sessionFiltering: m.sessionFiltering,
		expanded:         m.expanded,
	}
	if m.pending != nil {
		k.pending = true
		k.pendingName = m.pending.name
		k.pendingArgs = m.pending.args
	}

	// Legacy status has a 6-second display window that depends on now, not on
	// any version. Treat a live legacy status as time-sensitive as well.
	legacyStatusLive := m.status != "" && now.Sub(m.statusAt) < 6*time.Second
	noticeLive := m.notices.Len() > 0
	liveEntry := false
	for i := range m.entries {
		e := &m.entries[i]
		if (e.Kind == EntryReasoning && m.working) || isLiveToolEntry(*e) {
			liveEntry = true
			break
		}
	}
	k.timeSensitive = m.working || legacyStatusLive || noticeLive || liveEntry
	if k.timeSensitive {
		k.nowNanos = now.UnixNano()
	}
	return k
}

// frameCacheHit reports whether the frame rendered for prev can be reused for
// cur without repainting. Equal keys are the only safe case.
func frameCacheHit(prev, cur frameCacheKey) bool {
	return prev == cur
}

// isPureScrollChange reports whether cur differs from prev only by the
// transcript scroll offset: the content version and every other visual input
// are unchanged. Such a transition is a cache MISS — scrolling shifts the body
// window and the status-line "↑N" marker, so its bytes must be repainted.
func isPureScrollChange(prev, cur frameCacheKey) bool {
	if prev == cur {
		return false
	}
	cur.scroll = prev.scroll
	return prev == cur
}
