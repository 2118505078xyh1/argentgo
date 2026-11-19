package app

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/2118505078xyh1/argent-go/internal/cli"
	"github.com/2118505078xyh1/argent-go/internal/server"
	"github.com/2118505078xyh1/argent-go/internal/session"
)

// openOrCreateSession implements --session/--resume: an explicit id or session
// file is reopened, --resume picks the newest stored session, and only a plain
// run creates a fresh one. The returned handle is appended to in place.
func openOrCreateSession(store *session.Store, opts cli.Options, firstPrompt string) (*session.Session, error) {
	if id := strings.TrimSpace(opts.Session); id != "" {
		sess, err := store.OpenSession(sessionIDFromArg(store, id))
		if err != nil {
			return nil, fmt.Errorf("resume session %q: %w", id, err)
		}
		return sess, nil
	}
	if opts.Resume {
		metas, err := store.List()
		if err != nil {
			return nil, fmt.Errorf("list sessions for --resume: %w", err)
		}
		if len(metas) > 0 {
			sess, err := store.OpenSession(metas[0].ID)
			if err != nil {
				return nil, fmt.Errorf("resume session %q: %w", metas[0].ID, err)
			}
			return sess, nil
		}
	}
	title := firstLine(firstPrompt, 60)
	if title == "" {
		title = "session"
	}
	sess, err := store.NewSession(title)
	if err != nil {
		return nil, fmt.Errorf("new session: %w", err)
	}
	return sess, nil
}

// sessionIDFromArg accepts a bare id (or unique prefix) and a path to a file
// inside the session store. Paths outside the store are refused so a crafted
// --session value cannot point at an arbitrary file.
func sessionIDFromArg(store *session.Store, value string) string {
	separator := strings.ContainsAny(value, `/\`)
	if !separator && !strings.HasSuffix(strings.ToLower(value), ".jsonl") {
		return value
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return value
	}
	dir := filepath.Clean(store.Dir())
	if filepath.Clean(filepath.Dir(absolute)) != dir {
		return value // OpenSession will reject the separators in the path.
	}
	return strings.TrimSuffix(filepath.Base(absolute), filepath.Ext(absolute))
}

// Sessions returns the session store adapter used by `argent sessions` and the
// RPC/ACP transports.
func Sessions(storage string) cli.SessionStore {
	return &sessionStore{dir: filepath.Join(storage, "sessions")}
}

type sessionStore struct {
	dir string
}

func (s *sessionStore) List() ([]server.SessionInfo, error) {
	metas, err := session.List(s.dir)
	if err != nil {
		return nil, err
	}
	out := make([]server.SessionInfo, 0, len(metas))
	for _, meta := range metas {
		out = append(out, server.SessionInfo{ID: meta.ID, Title: meta.Title, Updated: meta.Updated})
	}
	return out, nil
}

func (s *sessionStore) Load(id string) ([]server.SessionEvent, error) {
	sess, err := session.OpenSession(s.dir, id)
	if err != nil {
		return nil, err
	}
	records, err := sess.Records()
	if err != nil {
		return nil, err
	}
	events := make([]server.SessionEvent, 0, len(records))
	for _, record := range records {
		events = append(events, server.SessionEvent{
			ID:     record.ID,
			Parent: record.Parent,
			Kind:   record.Kind,
			TS:     record.TS,
			Data:   record.Data,
		})
	}
	return events, nil
}

func (s *sessionStore) Delete(id string) error {
	if err := session.Delete(s.dir, id); err != nil {
		return fmt.Errorf("delete session %s: %w", id, err)
	}
	return nil
}
