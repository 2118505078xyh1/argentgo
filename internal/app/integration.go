package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/session"
)

// checkpointTracker collects the pre-turn manifests the agent asks for before
// each mutating tool batch. The manifest parent is captured at hook time (the
// leaf before the batch) while the post-turn hashes are filled after the run,
// so both retry-fork and undo see the state they need.
type checkpointTracker struct {
	root    string
	sess    *session.Session
	pending []session.Checkpoint
}

func newCheckpointTracker(root string, sess *session.Session) *checkpointTracker {
	return &checkpointTracker{root: root, sess: sess}
}

func (t *checkpointTracker) capture(cp agent.Checkpoint, prompt string) {
	if t == nil || len(cp.Paths) == 0 {
		return
	}
	captured, err := session.CaptureCheckpoint(t.root, cp.Paths)
	if err != nil || len(captured.Files) == 0 {
		return
	}
	captured.Prompt = prompt
	records, err := t.sess.Records()
	if err == nil && len(records) > 0 {
		captured.Parent = records[len(records)-1].ID
	}
	t.pending = append(t.pending, captured)
}

func (t *checkpointTracker) finish(partial bool) {
	if t == nil {
		return
	}
	for i := range t.pending {
		cp := &t.pending[i]
		cp.Partial = partial
		if err := session.CompleteCheckpoint(t.root, cp); err != nil {
			continue
		}
		_, _ = t.sess.AppendRecord(session.KindCheckpoint, *cp)
	}
	t.pending = nil
}

// recordAttempt maps a terminal run error to the session attempt reason the
// history/replay layer expects. Successful and retried attempts are recorded
// by the session package itself.
func recordAttempt(sess *session.Session, ctx context.Context, err error) {
	if sess == nil || err == nil {
		return
	}
	reason := session.AttemptFailed
	switch {
	case errors.Is(err, context.Canceled) || ctx.Err() != nil:
		reason = session.AttemptCancelled
	case errors.Is(err, llm.ErrStreamError):
		reason = session.AttemptStreamError
	}
	_, _ = sess.RecordAttemptError(reason, err)
}

// mutationPaths routes a mutating tool call to the registry that owns its
// workspace root and returns the checkpoint paths from that registry.
func (m *multiRootTools) mutationPaths(name, args string) []string {
	index, routed, err := m.route(name, args)
	if err != nil || index < 0 || index >= len(m.regs) {
		return nil
	}
	return m.regs[index].MutationPaths(name, routed)
}

// compactWithEvidence writes a compaction record that keeps the runtime
// failures and next-step lines the agent collected, in addition to whatever
// the session summarizer extracted from the dropped messages.
func compactWithEvidence(sess *session.Session, evidence agent.CompactionEvidence) {
	if sess == nil {
		return
	}
	entries := sess.Messages()
	start := len(entries) - defaultCompactKeep
	for start > 0 && start < len(entries) && entries[start].Role == "tool" {
		start--
	}
	if start <= 0 {
		return
	}
	compaction := session.Summarize(entries[:start], len(entries)-start)
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
	compaction.Summary = buildCompactionSummary(compaction)
	_ = sess.Append(session.KindCompaction, compaction)
}

func containsLine(lines []string, want string) bool {
	for _, line := range lines {
		if line == want {
			return true
		}
	}
	return false
}

func buildCompactionSummary(c session.Compaction) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Compaction\n- dropped %d message(s), kept the last %d\n", c.Dropped, c.Keep)
	writeSummarySection(&b, "Goal", nil)
	writeSummarySection(&b, "Files changed", c.Files)
	writeSummarySection(&b, "Failures", c.Failures)
	writeSummarySection(&b, "Next steps", c.NextSteps)
	return b.String()
}

func writeSummarySection(b *strings.Builder, title string, items []string) {
	fmt.Fprintf(b, "\n## %s\n", title)
	if len(items) == 0 {
		b.WriteString("- (none)\n")
		return
	}
	for _, item := range items {
		fmt.Fprintf(b, "- %s\n", item)
	}
}
