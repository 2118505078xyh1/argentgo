package app

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/session"
	"github.com/2118505078xyh1/argentgo/internal/tools"
)

// historySource adapts a session's JSONL transcript to the consumer-side
// tools.HistorySource interface. It lives in app so internal/tools does not
// need to import internal/session (layering rule).
type historySource struct {
	sess *session.Session
}

// Search returns a compact, bounded string of matching transcript lines for
// the active session. An empty query returns the most recent lines.
func (h *historySource) Search(query string, limit int) string {
	if h == nil || h.sess == nil {
		return ""
	}
	if limit <= 0 {
		limit = 20
	}
	const capLimit = 100
	if limit > capLimit {
		limit = capLimit
	}
	hits, err := session.Search(h.sess.Dir(), query, limit)
	if err != nil || len(hits) == 0 {
		return "no matching history"
	}
	type entry struct {
		Line    int    `json:"line"`
		Snippet string `json:"snippet"`
	}
	out := make([]entry, 0, len(hits))
	for _, hit := range hits {
		snippet := hit.Snippet
		// Collapse newlines and bound each snippet to keep the tool output compact.
		snippet = strings.Join(strings.Fields(snippet), " ")
		if len(snippet) > 240 {
			snippet = snippet[:240] + "…"
		}
		out = append(out, entry{Line: hit.Line, Snippet: snippet})
	}
	data, err := json.Marshal(out)
	if err != nil {
		return fmt.Sprintf("history: %v", err)
	}
	return string(data)
}

// wireHistory attaches the history tool to a registry once the session is
// known.
func wireHistory(registry *tools.Registry, sess *session.Session) {
	if registry == nil || sess == nil {
		return
	}
	registry.SetHistorySource(&historySource{sess: sess})
}
