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

// checkpointTracker 收集 agent 在每个变更型工具批次之前请求的轮次前清单。
// 清单的 parent 在钩子时刻捕获（批次之前的叶子节点），而轮次后的哈希在运行之后填充，
// 使重试分叉和撤销都能看到它们所需的状态。
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
	// parent 必须取活动路径的叶节点，与 RecordCheckpoint 的语义一致：
	// 分叉/恢复后文件顺序最后一条可能落在废弃分支上，不能用它作为重试分叉点。
	if parent, ok := t.sess.ActiveLeafID(); ok {
		captured.Parent = parent
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

// recordAttempt 把终结性的运行错误映射为历史/重放层所期望的会话尝试原因。
// 成功和重试的尝试由 session 包自己记录。
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

// mutationPaths 把变更型工具调用路由到拥有其工作区根目录的注册表，并从该注册表返回检查点路径。
func (m *multiRootTools) mutationPaths(name, args string) []string {
	index, routed, err := m.route(name, args)
	if err != nil || index < 0 || index >= len(m.regs) {
		return nil
	}
	return m.regs[index].MutationPaths(name, routed)
}

// compactWithEvidence 写入一条压缩记录，除会话摘要器从被丢弃消息中提取的内容之外，
// 还保留 agent 收集到的运行时失败和后续步骤行。
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
