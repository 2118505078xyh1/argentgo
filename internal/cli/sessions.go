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
	"strconv"
	"strings"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/server"
	"github.com/2118505078xyh1/argentgo/internal/session"
)

// runSessions implements `sessions list|show|rm|find|--grep|tree|export`.
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
	case "--grep":
		query, limit, err := parseGrepArgs(args[1:])
		if err != nil {
			fmt.Fprintf(app.IO.Err, "argent: %v\n", err)
			sessionsUsage(app.IO.Err)
			return 2
		}
		return sessionsGrep(app, store, query, limit)
	case "tree":
		if len(args) < 2 {
			sessionsUsage(app.IO.Err)
			return 2
		}
		return sessionsTree(app, store, args[1])
	case "undo":
		if len(args) < 2 {
			sessionsUsage(app.IO.Err)
			return 2
		}
		return sessionsUndo(app, args[1], args[2:])
	case "retry":
		if len(args) < 2 {
			sessionsUsage(app.IO.Err)
			return 2
		}
		return sessionsRetry(app, args[1], args[2:])
	case "name":
		if len(args) < 3 {
			sessionsUsage(app.IO.Err)
			return 2
		}
		return sessionsName(app, args[1], strings.Join(args[2:], " "))
	case "export":
		if len(args) < 2 {
			sessionsUsage(app.IO.Err)
			return 2
		}
		return sessionsExport(app, store, args[1], args[2:])
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

// sessionGrepper is the optional read side an injected store can provide. The
// fallback reads the JSONL files in the storage directory directly.
type sessionGrepper interface {
	Search(query string, limit int) ([]session.Hit, error)
}

// sessionTreeStore is the optional branch/export side of an injected store.
type sessionTreeStore interface {
	Branches(id string) ([]session.Branch, error)
	RenderHTML(id, branchID string, w io.Writer) error
}

func parseGrepArgs(args []string) (string, int, error) {
	limit := 20
	var words []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--limit":
			if i+1 >= len(args) {
				return "", 0, errors.New("--grep needs a value for --limit")
			}
			i++
			value, err := strconv.Atoi(args[i])
			if err != nil || value <= 0 {
				return "", 0, fmt.Errorf("invalid --limit %q", args[i])
			}
			limit = value
		case strings.HasPrefix(args[i], "--limit="):
			value, err := strconv.Atoi(strings.TrimPrefix(args[i], "--limit="))
			if err != nil || value <= 0 {
				return "", 0, fmt.Errorf("invalid --limit %q", args[i])
			}
			limit = value
		case strings.HasPrefix(args[i], "-"):
			return "", 0, fmt.Errorf("unknown --grep option %q", args[i])
		default:
			words = append(words, args[i])
		}
	}
	query := strings.Join(words, " ")
	if strings.TrimSpace(query) == "" {
		return "", 0, errors.New("--grep needs search text")
	}
	return query, limit, nil
}

func sessionsGrep(app App, store SessionStore, query string, limit int) int {
	var hits []session.Hit
	var err error
	if grepper, ok := store.(sessionGrepper); ok {
		hits, err = grepper.Search(query, limit)
	} else {
		hits, err = session.Search(app.sessionDir(), query, limit)
	}
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	if len(hits) == 0 {
		fmt.Fprintf(app.IO.Out, "no matches for %q\n", query)
		return 0
	}
	for _, hit := range hits {
		fmt.Fprintf(app.IO.Out, "%s:%d\t%s\t%s\t%s\n", hit.ID, hit.Line, hit.Path, formatTime(hit.TS), hit.Snippet)
	}
	return 0
}

func sessionsTree(app App, store SessionStore, id string) int {
	var branches []session.Branch
	var err error
	if trees, ok := store.(sessionTreeStore); ok {
		branches, err = trees.Branches(id)
	} else {
		var path string
		path, err = session.ResolvePath(app.sessionDir(), id)
		if err == nil {
			branches, err = session.Branches(path)
		}
	}
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	for _, branch := range branches {
		marker := " "
		if branch.Active {
			marker = "*"
		}
		link := "-"
		if branch.SourceSession != "" {
			link = branch.SourceSession
			if branch.SourceRecord != "" {
				link += "/" + branch.SourceRecord
			}
		}
		fmt.Fprintf(app.IO.Out, "%s%s\tparent=%s\tdepth=%d\tlink=%s\t%s\t%s\n",
			marker, branch.ID, branch.Parent, branch.Depth, link, branch.Kind, branch.Label)
	}
	return 0
}

func sessionsExport(app App, store SessionStore, id string, args []string) int {
	outPath, branchID := "", ""
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "-o" || args[i] == "--output":
			if i+1 >= len(args) {
				sessionsUsage(app.IO.Err)
				return 2
			}
			i++
			outPath = args[i]
		case strings.HasPrefix(args[i], "-o="):
			outPath = strings.TrimPrefix(args[i], "-o=")
		case strings.HasPrefix(args[i], "--output="):
			outPath = strings.TrimPrefix(args[i], "--output=")
		case args[i] == "--branch":
			if i+1 >= len(args) {
				sessionsUsage(app.IO.Err)
				return 2
			}
			i++
			branchID = args[i]
		default:
			fmt.Fprintf(app.IO.Err, "argent: unexpected export argument %q\n", args[i])
			return 2
		}
	}

	var w io.Writer = app.IO.Out
	if outPath != "" {
		file, err := os.Create(outPath)
		if err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		defer file.Close()
		w = file
	}

	var err error
	if trees, ok := store.(sessionTreeStore); ok {
		err = trees.RenderHTML(id, branchID, w)
	} else {
		var path string
		path, err = session.ResolvePath(app.sessionDir(), id)
		if err == nil {
			err = session.RenderHTML(path, branchID, w)
		}
	}
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	if outPath != "" {
		fmt.Fprintf(app.IO.Out, "wrote %s\n", outPath)
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
        argent sessions --grep <text> [--limit N]
        argent sessions tree <id>
        argent sessions undo <id> [-C <workspace>]
        argent sessions retry <id> [checkpoint-id]
        argent sessions name <id> <title>
        argent sessions export <id> [-o file.html]
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
				return session.TitleFromPrompt(text)
			}
		}
	}
	return ""
}
