package app

import (
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/session"
)

const (
	// defaultAutoCompactTokens matches the agent loop's context target: a
	// conversation that large is a good candidate for a summary record.
	defaultAutoCompactTokens = 32000
	// defaultCompactKeep leaves the most recent turns verbatim after compaction.
	defaultCompactKeep = 6
)

// compactThresholdFrom reads an optional nested {"compaction":{"threshold":N}}
// block from the merged config, so the trigger is configurable without a flag.
func compactThresholdFrom(raw map[string]any) int {
	if nested, ok := raw["compaction"].(map[string]any); ok {
		if value := intField(nested, "threshold", "tokens", "autoCompactTokens"); value > 0 {
			return value
		}
	}
	return 0
}

// sessionCompactor is the app-side implementation of agent.Compactor. It owns
// the session, so the agent package stays free of any session dependency.
type sessionCompactor struct {
	res  Resolved
	sess *session.Session
}

func newSessionCompactor(res Resolved, sess *session.Session) *sessionCompactor {
	return &sessionCompactor{res: res, sess: sess}
}

// CompactThreshold is the trigger the agent should use for mid-turn compaction.
// A negative configured value disables automatic compaction entirely.
func (c *sessionCompactor) CompactThreshold() int {
	if c == nil {
		return 0
	}
	threshold := c.res.AutoCompactTokens
	if threshold == 0 {
		threshold = defaultAutoCompactTokens
	}
	if threshold < 0 {
		return 0
	}
	return threshold
}

// MaybeCompact folds the older part of the conversation into a session
// compaction record and returns the folded view for the very next request. The
// window is the 0.8-band target supplied by the agent; the cut is derived from
// that token budget rather than a fixed message count.
func (c *sessionCompactor) MaybeCompact(messages []llm.Message, window int) ([]llm.Message, bool) {
	if c == nil || c.sess == nil || window <= 0 || len(messages) < 4 {
		return messages, false
	}
	entries := entriesFromMessages(messages)
	start := session.CutForBudget(entries, window)
	if start <= 0 {
		return messages, false
	}
	compaction := session.Summarize(entries[:start], len(messages)-start)
	// Mid-turn, the run's OnCompactionEvidence callback has not fired yet, so
	// collect next-step lines from the visible messages and let Summarize pick
	// up tool failures from the dropped entries.
	enrichCompaction(&compaction, agent.CollectCompactionEvidence(nil, messages))
	if err := c.sess.Append(session.KindCompaction, compaction); err != nil {
		return messages, false
	}

	out := make([]llm.Message, 0, len(messages)-start+2)
	out = append(out, llm.Message{Role: "system", Content: compaction.Summary})
	// The original system prompt is folded away by a plain session compaction;
	// keep it explicitly so the next model request still carries the rules.
	if messages[0].Role == "system" {
		out = append(out, messages[0])
	}
	out = append(out, messages[start:]...)
	return out, true
}

// entriesFromMessages adapts the agent's view to the session compaction API.
// Reconstructed entries carry only the fields compaction reads (role, content
// and tool-call id); tool names are not present on llm.Message.
func entriesFromMessages(messages []llm.Message) []session.Entry {
	entries := make([]session.Entry, 0, len(messages))
	for _, m := range messages {
		entries = append(entries, session.Entry{
			Role:       m.Role,
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
		})
	}
	return entries
}

// enrichCompaction carries runtime failures and next-step lines into a summary.
// It mirrors compactWithEvidence so mid-turn and post-run compactions preserve
// the same context on resume.
func enrichCompaction(compaction *session.Compaction, evidence agent.CompactionEvidence) {
	for _, failure := range agent.SortFailuresByTurn(evidence.Failures) {
		item := failure.Reason
		if failure.Tool != "" {
			item = failure.Tool + ": " + item
		}
		if item = strings.TrimSpace(item); item != "" && !containsLine(compaction.Failures, item) {
			compaction.Failures = append(compaction.Failures, item)
		}
	}
	for _, step := range evidence.NextSteps {
		if step = strings.TrimSpace(step); step != "" && !containsLine(compaction.NextSteps, step) {
			compaction.NextSteps = append(compaction.NextSteps, step)
		}
	}
	compaction.Summary = buildCompactionSummary(*compaction)
}

// maybeAutoCompact is the post-run step behind --no-auto-compact. It is a
// no-op when the flag is set or the conversation still fits the threshold.
// Runtime failures and next-step lines are carried into the summary so a
// resumed run does not lose the context it needs most.
func maybeAutoCompact(res Resolved, sess *session.Session, evidence agent.CompactionEvidence) {
	if res.NoAutoCompact || sess == nil {
		return
	}
	threshold := res.AutoCompactTokens
	if threshold == 0 {
		threshold = defaultAutoCompactTokens
	}
	if threshold < 0 {
		return
	}
	entries := sess.Messages()
	messages := make([]llm.Message, 0, len(entries))
	for _, entry := range entries {
		messages = append(messages, llm.Message{Role: entry.Role, Content: entry.Content})
	}
	if agent.EstimateMessages(messages) < threshold {
		return
	}
	hasEvidence := len(evidence.Failures) > 0 || len(evidence.NextSteps) > 0
	if hasEvidence {
		compactWithEvidence(sess, evidence)
		return
	}
	_ = sess.Compact(defaultCompactKeep)
}
