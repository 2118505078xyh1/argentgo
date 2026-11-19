package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/2118505078xyh1/argent-go/internal/server"
)

// runSessions implements `sessions list|show|rm|find`.
func runSessions(app App, _ context.Context, args []string) int {
	if len(args) == 0 {
		sessionsUsage(app.IO.Err)
		return 2
	}
	store := app.sessionStore()
	switch args[0] {
	case "-h", "--help":
		sessionsUsage(app.IO.Out)
		return 0
	case "list":
		return sessionsList(app, store)
	case "show":
		if len(args) < 2 {
			sessionsUsage(app.IO.Err)
			return 2
		}
		return sessionsShow(app, store, args[1])
	case "rm":
		if len(args) < 2 {
			sessionsUsage(app.IO.Err)
			return 2
		}
		if err := store.Delete(args[1]); err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		fmt.Fprintf(app.IO.Out, "removed %s\n", args[1])
		return 0
	case "find":
		if len(args) < 2 {
			sessionsUsage(app.IO.Err)
			return 2
		}
		return sessionsFind(app, store, strings.Join(args[1:], " "))
	default:
		fmt.Fprintf(app.IO.Err, "argent: unknown sessions command %q\n", args[0])
		sessionsUsage(app.IO.Err)
		return 2
	}
}

func (a App) sessionStore() SessionStore {
	if a.Sessions != nil {
		return a.Sessions
	}
	return &fsStore{dir: a.sessionDir()}
}

func sessionsList(app App, store SessionStore) int {
	infos, err := store.List()
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	sortSessions(infos)
	for _, info := range infos {
		fmt.Fprintf(app.IO.Out, "%s\t%s\t%s\n", info.ID, formatTime(info.Updated), info.Title)
	}
	return 0
}

func sessionsFind(app App, store SessionStore, keyword string) int {
	infos, err := store.List()
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	needle := strings.ToLower(keyword)
	sortSessions(infos)
	found := 0
	for _, info := range infos {
		if strings.Contains(strings.ToLower(info.ID), needle) || strings.Contains(strings.ToLower(info.Title), needle) {
			fmt.Fprintf(app.IO.Out, "%s\t%s\t%s\n", info.ID, formatTime(info.Updated), info.Title)
			found++
		}
	}
	if found == 0 {
		fmt.Fprintf(app.IO.Out, "no sessions matching %q\n", keyword)
	}
	return 0
}

func sessionsShow(app App, store SessionStore, id string) int {
	events, err := store.Load(id)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	for _, event := range events {
		line, err := json.Marshal(event)
		if err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		fmt.Fprintln(app.IO.Out, string(line))
	}
	return 0
}

func sortSessions(infos []server.SessionInfo) {
	sort.SliceStable(infos, func(i, j int) bool {
		return infos[i].Updated.After(infos[j].Updated)
	})
}

func formatTime(ts time.Time) string {
	if ts.IsZero() {
		return "-"
	}
	return ts.UTC().Format(time.RFC3339)
}

func sessionsUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argent sessions list
       argent sessions show <id>
       argent sessions rm <id>
       argent sessions find <keyword>
`)
}

// fsStore reads JSONL session files directly from the storage directory. It is
// the fallback used until the integrator injects internal/session.
type fsStore struct {
	dir string
}

func (s *fsStore) List() ([]server.SessionInfo, error) {
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var infos []server.SessionInfo
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".jsonl")
		info, err := entry.Info()
		if err != nil {
			continue
		}
		events, err := s.Load(id)
		if err != nil {
			continue
		}
		infos = append(infos, server.SessionInfo{
			ID:      id,
			Title:   sessionTitle(events),
			Updated: info.ModTime(),
		})
	}
	return infos, nil
}

func (s *fsStore) Load(id string) ([]server.SessionEvent, error) {
	path, err := s.path(id)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load session %q: %w", id, err)
	}
	var events []server.SessionEvent
	for number, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var event server.SessionEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, number+1, err)
		}
		events = append(events, event)
	}
	return events, nil
}

func (s *fsStore) Delete(id string) error {
	path, err := s.path(id)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove session %q: %w", id, err)
	}
	return nil
}

func (s *fsStore) path(id string) (string, error) {
	if !safeSessionID(id) {
		return "", fmt.Errorf("invalid session id %q", id)
	}
	return filepath.Join(s.dir, id+".jsonl"), nil
}

func safeSessionID(id string) bool {
	if id == "" || id == "." || id == ".." {
		return false
	}
	if strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return false
	}
	return true
}

// sessionTitle finds the first human-readable text in a session.
func sessionTitle(events []server.SessionEvent) string {
	for i, event := range events {
		if i >= 40 {
			break
		}
		var payload map[string]any
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			continue
		}
		for _, key := range []string{"text", "prompt", "content", "message"} {
			if text, ok := payload[key].(string); ok {
				text = strings.Join(strings.Fields(text), " ")
				if text == "" {
					continue
				}
				if len(text) > 60 {
					text = text[:59] + "…"
				}
				return text
			}
		}
	}
	return ""
}
