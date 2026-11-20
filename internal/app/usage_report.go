package app

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/session"
)

// modelUsage is one model's aggregated share of a session's per-request usage
// records. Model names are kept verbatim (including CJK characters) so the
// report names the model that actually served the requests.
type modelUsage struct {
	Model  string
	Input  int
	Output int
	Cached int
	Calls  int
}

// cumulativeSessionUsage is the production reader for the session package's
// cumulative APIs. AggregateStats supplies the cross-resume token/cost roll-up,
// and UsageRecords supplies the per-model breakdown from raw per-request facts.
// A session with no stats records yields zero totals; a session with no usage
// records yields an empty breakdown. Both are valid states, not errors.
//
// This is the app-side consumer that gives session.AggregateStats/UsageRecords
// a real production call path: without it those APIs were only exercised by
// session's own tests, so a resumed session's reported usage could silently be
// the last run's value rather than the accumulated total.
func cumulativeSessionUsage(sess *session.Session) (session.SessionStats, []modelUsage) {
	if sess == nil {
		return session.SessionStats{}, nil
	}
	total := sess.AggregateStats()

	byModel := make(map[string]*modelUsage)
	for _, rec := range sess.UsageRecords() {
		model := rec.Model
		if model == "" {
			model = "(unknown)"
		}
		entry := byModel[model]
		if entry == nil {
			entry = &modelUsage{Model: model}
			byModel[model] = entry
		}
		entry.Input += rec.Input
		entry.Output += rec.Output
		entry.Cached += rec.Cached
		entry.Calls++
	}

	out := make([]modelUsage, 0, len(byModel))
	for _, entry := range byModel {
		out = append(out, *entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return total, out
}

// appendSessionCumulative adds a cross-resume usage block to --tokens output.
// It runs only when --session addresses a session, so a plain `--tokens` call
// keeps its exact existing output and no agent event or protocol shape changes.
func appendSessionCumulative(b *strings.Builder, res Resolved) error {
	id := strings.TrimSpace(res.Options.Session)
	if id == "" {
		return nil
	}
	store, err := session.Open(filepath.Join(res.Storage, "sessions"))
	if err != nil {
		return fmt.Errorf("open session store: %w", err)
	}
	sess, err := store.OpenSession(sessionIDFromArg(store, id))
	if err != nil {
		return fmt.Errorf("resume session %q: %w", id, err)
	}
	total, models := cumulativeSessionUsage(sess)

	fmt.Fprintf(b, "\nsession %s cumulative (persisted stats):\n", sess.ID())
	fmt.Fprintf(b, "  prompt=%d completion=%d cached=%d calls=%d cost=$%.4f",
		total.PromptTokens, total.CompletionTokens, total.CachedTokens, total.Calls, total.CostUSD)
	if total.CostSource != "" {
		fmt.Fprintf(b, " (%s)", total.CostSource)
	}
	fmt.Fprintln(b)
	if len(models) == 0 {
		fmt.Fprintln(b, "  models: (no usage records)")
		return nil
	}
	for _, m := range models {
		fmt.Fprintf(b, "  model[%s]: input=%d output=%d cached=%d calls=%d\n",
			m.Model, m.Input, m.Output, m.Cached, m.Calls)
	}
	return nil
}
