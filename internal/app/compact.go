package app

import (
	"encoding/json"
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

// compactionInputs resolves the (contextWindow, configuredTokens, percent)
// triple that drives the compaction trigger. A percentage is ignored when no
// context window is known, so the trigger falls back to the configured token
// count or the compiled default instead of collapsing to 1.
func compactionInputs(res Resolved) (window, configured, percent int) {
	window = res.Cfg.ContextWindow
	configured = res.AutoCompactTokens
	percent = res.Cfg.CondensePercent(res.Profile)
	if window <= 0 {
		percent = 0
	}
	return
}

// compactionDecision is the pure estimate-vs-trigger comparison shared by the
// compact paths. It is factored out so the context-window percentage math and the
// 1.3 inflation can be tested with a stub estimate and no model/session calls.
func compactionDecision(estimate, window, configured, percent int) (threshold int, compact bool) {
	threshold = agent.CompactionThreshold(window, configured, percent)
	if threshold <= 0 {
		return threshold, false
	}
	return threshold, agent.InflateEstimate(estimate) >= threshold
}

// CompactThreshold is the trigger the agent should use for mid-turn compaction.
// A negative configured value disables automatic compaction entirely.
func (c *sessionCompactor) CompactThreshold() int {
	if c == nil {
		return 0
	}
	if c.res.AutoCompactTokens < 0 {
		return 0
	}
	window, configured, percent := compactionInputs(c.res)
	return agent.CompactionThreshold(window, configured, percent)
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
// The reconstructed entries carry the fields compaction reads: role, content,
// the tool-call id on results, and the full tool-call payload on assistant
// messages. The payload uses the same shape the session records persist —
// "tool_calls" with one object per call carrying "id" — so adjustCut can pair
// a call with its result instead of silently dropping the ids. File targets
// found in tool arguments are normalised into a "files" array, which the
// session path scanner already understands.
func entriesFromMessages(messages []llm.Message) []session.Entry {
	entries := make([]session.Entry, 0, len(messages))
	for _, m := range messages {
		entry := session.Entry{
			Role:       m.Role,
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
		}
		if len(m.ToolCalls) > 0 {
			payload := map[string]any{"tool_calls": m.ToolCalls}
			if files := toolCallPaths(m.ToolCalls); len(files) > 0 {
				payload["files"] = files
			}
			if data, err := json.Marshal(payload); err == nil {
				entry.Data = data
			}
		}
		entries = append(entries, entry)
	}
	return entries
}

// pathArgumentKeys lists the argument keys producers use for a file target.
// The session path scanner recognises "path"/"file"/"files" but not the
// Claude-style "file_path"/"filePath" variants, so the adapter normalises them
// into a "files" array rather than duplicating that scanner in this package.
var pathArgumentKeys = map[string]bool{
	"file_path": true, "filePath": true, "filepath": true,
	"file_name": true, "filename": true,
	"path": true, "file": true, "files": true,
}

// toolCallPaths collects distinct file paths declared by a batch of tool
// calls. Arguments are raw JSON strings and may themselves contain JSON
// strings, so nested objects are visited recursively.
func toolCallPaths(calls []llm.ToolCall) []string {
	out, seen := make([]string, 0), make(map[string]bool)
	for _, call := range calls {
		arguments := call.Function.Arguments
		if strings.TrimSpace(arguments) == "" {
			continue
		}
		var value any
		if json.Unmarshal([]byte(arguments), &value) == nil {
			collectArgumentPaths(value, &out, seen)
		}
	}
	return out
}

func collectArgumentPaths(value any, out *[]string, seen map[string]bool) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if pathArgumentKeys[key] {
				switch paths := child.(type) {
				case string:
					addArgumentPath(out, seen, paths)
				case []any:
					for _, item := range paths {
						if text, ok := item.(string); ok {
							addArgumentPath(out, seen, text)
						}
					}
				}
			}
			if text, ok := child.(string); ok {
				var inner any
				if json.Unmarshal([]byte(text), &inner) == nil {
					collectArgumentPaths(inner, out, seen)
				}
				continue
			}
			collectArgumentPaths(child, out, seen)
		}
	case []any:
		for _, item := range typed {
			collectArgumentPaths(item, out, seen)
		}
	}
}

func addArgumentPath(out *[]string, seen map[string]bool, path string) {
	path = strings.TrimSpace(path)
	if path == "" || seen[path] {
		return
	}
	seen[path] = true
	*out = append(*out, path)
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
	if res.AutoCompactTokens < 0 {
		return
	}
	entries := sess.Messages()
	messages := make([]llm.Message, 0, len(entries))
	for _, entry := range entries {
		messages = append(messages, llm.Message{Role: entry.Role, Content: entry.Content})
	}
	window, configured, percent := compactionInputs(res)
	if _, compact := compactionDecision(agent.EstimateMessages(messages), window, configured, percent); !compact {
		return
	}
	hasEvidence := len(evidence.Failures) > 0 || len(evidence.NextSteps) > 0
	if hasEvidence {
		compactWithEvidence(sess, evidence)
		return
	}
	_ = sess.Compact(defaultCompactKeep)
}
