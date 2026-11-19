package app

import (
	"fmt"
	"path/filepath"

	"github.com/2118505078xyh1/argent-go/internal/cli"
	"github.com/2118505078xyh1/argent-go/internal/server"
	"github.com/2118505078xyh1/argent-go/internal/session"
)

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
