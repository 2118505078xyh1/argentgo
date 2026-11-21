package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/session"
	"github.com/2118505078xyh1/argentgo/internal/tools"
)

// checkpointTracker 收集 agent 在每个变更型工具批次之前请求的轮次前清单。
// 清单的 parent 在钩子时刻捕获（批次之前的叶子节点），而轮次后的哈希在运行之后填充，
// 使重试分叉和撤销都能看到它们所需的状态。
// batchDiff 保存一个工具批次的独立前态。turn 在一次 Run 内唯一，因此每个
// value 自带自己的前字节与哈希，批次之间绝不共享——逆序 undo 不会因哈希
// 不匹配而 ErrCheckpointConflict。
type batchDiff struct {
	before   map[string]string  // SnapshotWorkspace 哈希，交给 DiscoverWorkspaceChanges
	cp       session.Checkpoint // CaptureCheckpoint 的全字节前态
	declared map[string]bool    // cp.Paths 声明路径，批次后与 changed 取并集
}

type checkpointTracker struct {
	root    string
	sess    *session.Session
	pending []session.Checkpoint

	// workspaceDiff 为 opt-in（ARGENT_CHECKPOINT_WORKSPACE_DIFF）。关闭时
	// capture/finish 走原有逐字节路径，pendingByTurn 不被使用。
	workspaceDiff bool
	pendingByTurn map[int]*batchDiff
}

func newCheckpointTracker(root string, sess *session.Session) *checkpointTracker {
	return &checkpointTracker{root: root, sess: sess, pendingByTurn: make(map[int]*batchDiff)}
}

func (t *checkpointTracker) capture(cp agent.Checkpoint, prompt string) {
	if t == nil {
		return
	}
	// opt-in 分支：绕过空 Paths 早退，即使 cp.Paths 为空也抓全字节，
	// 否则 shell 间接改动在批次后才可知，没有前态可供撤销。
	if t.workspaceDiff {
		t.captureWorkspaceDiff(cp, prompt)
		return
	}
	// 默认分支：以下逻辑逐字节保持原样（含 len(cp.Paths)==0 早退）。
	if len(cp.Paths) == 0 {
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

// captureWorkspaceDiff 在 opt-in 下为整个工作区抓前态（全字节 + 哈希）。
func (t *checkpointTracker) captureWorkspaceDiff(cp agent.Checkpoint, prompt string) {
	paths := tools.WorkspaceFilePaths(t.root)
	if len(paths) >= tools.WorkspaceDiffMaxEnumerateFiles {
		// 超大工作区：拒绝生成只含前 N 个文件的不完整前态，回退声明路径（fail-closed）。
		paths = cp.Paths
	}
	before, err := tools.SnapshotWorkspace(t.root)
	if err != nil {
		return
	}
	captured, err := session.CaptureCheckpoint(t.root, paths)
	if err != nil || len(captured.Files) == 0 {
		return
	}
	declared := make(map[string]bool, len(cp.Paths))
	for _, p := range cp.Paths {
		declared[p] = true
	}
	// Parent/Prompt 必须在批次前取：批次内 finishCall 已把 tool 记录写入会话，
	// 批次后再取 ActiveLeafID 会落到工具记录上，分叉点就错了。
	captured.Prompt = prompt
	if parent, ok := t.sess.ActiveLeafID(); ok {
		captured.Parent = parent
	}
	t.pendingByTurn[cp.Turn] = &batchDiff{before: before, cp: captured, declared: declared}
}

// afterBatch 由 agent.Options.AfterBatch 在 runTools 出口调用，把该批次实际
// 改动（含未声明路径）并入检查点。默认（workspaceDiff=false）时永不产生效果。
func (t *checkpointTracker) afterBatch(turn int) {
	if t == nil || !t.workspaceDiff {
		return
	}
	e := t.pendingByTurn[turn]
	if e == nil {
		return // 无变更/被整体拒绝的批次：capture 未登记，整批拒绝也走这里
	}
	changed := tools.DiscoverWorkspaceChanges(t.root, e.before)
	keep := make(map[string]bool, len(changed)+len(e.declared))
	for _, p := range changed {
		keep[p] = true
	}
	for p := range e.declared {
		keep[p] = true
	}
	files := e.cp.Files[:0]
	for _, f := range e.cp.Files {
		if keep[f.Path] {
			files = append(files, f)
		}
	}
	e.cp.Files = files
	delete(t.pendingByTurn, turn)
	if len(e.cp.Files) == 0 {
		return // 批次没有任何改动：不写空检查点
	}
	t.pending = append(t.pending, e.cp)
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
	if t.workspaceDiff {
		t.pendingByTurn = make(map[int]*batchDiff)
	}
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
func compactWithEvidence(sess *session.Session, evidence agent.CompactionEvidence, reg *OptInRegistry, summaryDep *sessionCompactor) {
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
	// 重建前取原始 Summarize 摘要，供 opt-in wire 恢复 Goal/Tool activity 段。
	orig := compaction.Summary
	compaction.Summary = rebuildCompactionSummary(compaction, entries[:start], orig, reg)
	// 模型摘要（opt-in）：轮后路径与轮中对称。默认（summaryDep nil 或缺省）时原样返回。
	if summaryDep != nil {
		compaction.Summary = applyModelSummaryWire(compaction.Summary, summaryDep.reg, summaryDep.ctx,
			summaryDep.chatter, summaryDep.summaryModel, compaction.Failures, compaction.NextSteps,
			compaction.Files, summaryDep.emit)
	}
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

// rebuildCompactionSummary 是轮中/轮后两条压缩路径共用的摘要重建入口。
// 它在 buildCompactionSummary 的结构化重建之上，按 opt-in 叠加三个压缩 wire：
// 恢复原始 Goal/Tool activity 段、注入隐式上下文、追加保真告警。
//
// reg 为 nil 或三个开关全关时，三个闭包的行为都是透传（fidelity 为 nil 直接跳过），
// 结果与直接调用 buildCompactionSummary 逐字节相同——默认路径不受影响。
// orig 必须是重建前的 Summarize 原始摘要（由调用方在覆盖前取），dropped 为被丢弃条目。
func rebuildCompactionSummary(c session.Compaction, dropped []session.Entry, orig string, reg *OptInRegistry) string {
	rebuilt := buildCompactionSummary(c)
	preserve := func(r, original string) string { return preserveSummarySections(r, original, reg) }
	enrich := func(r string, d []session.Entry) string { return enrichWithImplicit(r, d, reg) }
	fidelity := newCompactionFidelityObserver(reg)
	return session.RebuildSummaryWithWires(rebuilt, session.RebuildInput{
		Compaction: c, OriginalSummary: orig, Dropped: dropped,
	}, preserve, enrich, fidelity)
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
