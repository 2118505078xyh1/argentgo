package app

import (
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
