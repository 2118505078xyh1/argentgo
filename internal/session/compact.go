package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// ArchiveRange 是一个半开字节区间 [Start, End)，用于定位压缩归档中的一条消息。
// 读取器可以定位到 Start 并读取 End-Start 个字节，逐字还原该消息，而无需加载整个归档。
// ID/Role 从原始条目复制而来，因此无需解码载荷即可将区间与摘要中的项目符号匹配。
type ArchiveRange struct {
	ID    string `json:"id,omitempty"`
	Role  string `json:"role,omitempty"`
	Start int64  `json:"start"`
	End   int64  `json:"end"`
}

// ArchiveRef 是指向压缩所丢弃消息的无损归档的可选指针。Path 相对于会话存储目录
// （即存放 <id>.jsonl 转录的同一目录），因此记录可以携带它而无需嵌入特定于机器的绝对路径。
// Bytes 是归档文件的大小；Ranges 按顺序将每条被丢弃的消息映射到其在文件中的字节区间。
type ArchiveRef struct {
	Path   string         `json:"path"`
	Bytes  int64          `json:"bytes"`
	Ranges []ArchiveRange `json:"ranges,omitempty"`
}

// Compaction 是 kind:"compaction" 记录的载荷。summary 替换对话中被丢弃的部分；
// Keep 表示仍逐字保留在其后的最近消息数量。
//
// Archive 是附加且可选的字段（omitempty）：较旧的记录没有 archive 字段，读取方式与以前完全相同。
// 现有的压缩行为从不设置它；写入方通过调用 WriteCompactionArchive / WriteCompactionArchiveRef 来选择启用。
type Compaction struct {
	Summary   string   `json:"summary"`
	Dropped   int      `json:"dropped"`
	Keep      int      `json:"keep"`
	Files     []string `json:"files,omitempty"`
	Failures  []string `json:"failures,omitempty"`
	NextSteps []string `json:"nextSteps,omitempty"`
	// Todo 保存压缩时的任务列表快照（[x]/[>]/[ ] 行）。omitempty 保证未启用
	// 或不存在的会话没有该字段，逐字节兼容旧记录。
	Todo    []string    `json:"todo,omitempty"`
	Archive *ArchiveRef `json:"archive,omitempty"`

	// SummaryEmpty 标记摘要是否无实质内容（仅标题或占位符），供上层据此决定是否
	// 重试模型摘要。它只在 opt-in 开启时由 Summarize 计算；json:"-" 保证它从不
	// 写入 JSONL，因此 opt-in 关闭（乃至开启）时压缩记录的字节都不受影响。
	SummaryEmpty bool `json:"-"`
}

// Compact 用一条摘要记录替换对话中较旧的部分，并保留最后 keep 条消息。
// 当历史记录已经放得下时，此操作为空操作。
func (s *Session) Compact(keep int) error {
	if keep < 0 {
		return fmt.Errorf("session: keep must be >= 0, got %d", keep)
	}
	entries := s.Messages()
	if len(entries) <= keep {
		return nil
	}
	start := adjustCut(entries, len(entries)-keep)
	if start <= 0 {
		return nil
	}
	return s.appendCompaction(entries, start)
}

// CompactBudget 压缩对话中较旧的部分，使保留的后缀能够容纳在估算的 token 预算内。
// 原有的 Compact(keep) 签名保持不变；这是用于轮中压缩的 token 预算重载，
// 在工具输出长度可变的场景下，固定的消息数量没有意义。
func (s *Session) CompactBudget(budget int) error {
	if budget <= 0 {
		return fmt.Errorf("session: compaction budget must be > 0, got %d", budget)
	}
	entries := s.Messages()
	start := CutForBudget(entries, budget)
	if start <= 0 {
		return nil
	}
	return s.appendCompaction(entries, start)
}

// compactionEventEnv 是"压缩事件"落盘的 opt-in 环境变量。仅当值恰为 "1"（允许首尾空白）时，
// appendCompaction 才在 KindCompaction 记录之后追加一条 KindCondensation 记录；
// 默认关闭，使压缩写入的字节与旧版逐字一致。
const compactionEventEnv = "ARGENT_COMPACTION_EVENT"

// KindCondensation 是压缩事件的记录 kind。它随 ARGENT_COMPACTION_EVENT=1 由 appendCompaction
// 追加，载荷为 CondensationEvent.Marshal() 的字节；messageEntries 会忽略它，
// 因此不会进入模型可见的对话。
const KindCondensation = "condensation"

// compactionEventEnabled 报告是否开启压缩事件落盘。每次调用都读取环境变量，
// 便于测试用 t.Setenv 精确控制，且不引入可变全局状态。
func compactionEventEnabled() bool {
	return strings.TrimSpace(os.Getenv(compactionEventEnv)) == "1"
}

// appendCompaction 写入摘要记录，然后重新发出被丢弃前缀中任何带有保留标记的记录，
// 使其得以保留（SelectForCompaction）。带标记的工具调用/结果会与其配对项一起重新发出，
// 因此压缩绝不会重新引入孤立的结果，也不会留下没有结果的调用。
// 当没有任何记录被标记时，此行为与旧版逐字节一致：只写入一条压缩记录，别无其他。
// 开启 ARGENT_COMPACTION_EVENT=1 时，在压缩记录之后额外追加一条 KindCondensation 记录；
// 关闭时整段被跳过，上面写入的字节与旧版完全一致。
func (s *Session) appendCompaction(entries []Entry, start int) error {
	restored := retainedForCompaction(entries, start)
	// Messages() 只把最新压缩记录投影为 system Entry，更早的 KindCompaction 记录会被
	// messageEntries 跳过。它们带有 RetainAlways 却永远进不了 Messages() 派生的 entries，
	// 主视图因此丢失其摘要文本。补发这些记录，并按载荷去重避免重复重发。
	restored = append(restored, s.retainedCompactionRecords(entries, start, restored)...)
	compaction := Summarize(entries[:start], len(entries)-start)
	if err := s.Append(KindCompaction, compaction); err != nil {
		return err
	}
	// opt-in：把本轮压缩记录为一等事件，使遗忘行为可解释、可复现。
	// 载荷用 CondensationEvent.Marshal() 的原始字节（json.RawMessage 不再编码），
	// 从而保留 CJK 原文与稳定的字段顺序；默认关闭时这里完全不执行。
	if compactionEventEnabled() {
		event := NewCondensationEvent(forgottenIDsForCompaction(entries, start), compaction.Summary, start)
		if err := s.Append(KindCondensation, json.RawMessage(event.Marshal())); err != nil {
			return err
		}
	}
	for _, record := range restored {
		if _, err := s.AppendRecord(record.Kind, record.Data); err != nil {
			return err
		}
	}
	return nil
}

// forgottenIDsForCompaction 返回本轮压缩从活动上下文中移除的记录 ID，即被丢弃前缀的全部 ID。
// 即便某条内容因保留标记被重新发出，它也会获得新的记录 ID，原 ID 不再存在于活动路径中，
// 因此仍视为被遗忘。空 ID 被跳过；去重与字典序排序交给 NewCondensationEvent 完成，
// 保证事件逐字节稳定。
func forgottenIDsForCompaction(entries []Entry, start int) []string {
	ids := make([]string, 0, start)
	for _, entry := range entries[:start] {
		if entry.ID != "" {
			ids = append(ids, entry.ID)
		}
	}
	return ids
}

// retainedCompactionRecords 从原始转录中找出切分点之前、带保留标记且被 Messages() 投影隐藏的
// KindCompaction 记录，以 KindSummary 的形式补发。Messages() 只对最新压缩记录生成 system Entry，
// 更早的压缩记录既不在 entries 里，也无法被 retainedForCompaction 看到；本函数弥补这个缺口。
//
// boundary 取第一条保留条目在原始路径中的位置；早于它的记录都属于丢弃区。restored 是
// retainedForCompaction 已选出的记录，按载荷字节去重以免把同一条压缩发出两次。
func (s *Session) retainedCompactionRecords(entries []Entry, start int, restored []Record) []Record {
	records, err := s.Records()
	if err != nil || len(records) == 0 {
		return nil
	}
	path := s.activePath(records)

	keptIDs := make(map[string]bool, len(entries)-start)
	for _, entry := range entries[start:] {
		keptIDs[entry.ID] = true
	}
	boundary := len(path)
	for i, record := range path {
		if keptIDs[record.ID] {
			boundary = i
			break
		}
	}

	seen := make(map[string]bool, len(restored)+len(path))
	for _, record := range restored {
		seen[string(record.Data)] = true
	}
	// 之前压缩已重新发出过的压缩记录也不再重复发出。
	for _, record := range path {
		if record.Kind == KindSummary {
			seen[string(record.Data)] = true
		}
	}

	var out []Record
	for i := 0; i < boundary; i++ {
		record := path[i]
		if record.Kind != KindCompaction || !GetRetention(record.Data).SurvivesCompaction() {
			continue
		}
		if seen[string(record.Data)] {
			continue
		}
		out = append(out, Record{Kind: KindSummary, Data: record.Data})
	}
	return out
}

// retainedForCompaction 按原始顺序返回被丢弃前缀中带标记的记录，并扩展为完整的工具对。
// SelectForCompaction 只检查 Record.Data，因此将 Entry 投影为最小化的 Record 就已足够。
func retainedForCompaction(entries []Entry, start int) []Record {
	dropped := entries[:start]
	records := make([]Record, len(dropped))
	for i, entry := range dropped {
		records[i] = Record{ID: entry.ID, Data: entry.Data}
	}
	// 不设上限：保留提示必须优先于保持压缩的低成本。
	flagged := SelectForCompaction(records, 0)
	if len(flagged) == 0 {
		return nil
	}
	flaggedIDs := make(map[string]bool, len(flagged))
	for _, record := range flagged {
		flaggedIDs[record.ID] = true
	}

	// 为被丢弃前缀内的工具对建立索引，这样带标记的一半会把另一半一起带入，
	// 而不会在保留的上下文中留下孤立的一半。
	calls := make(map[string][]int)
	results := make(map[string][]int)
	for i, entry := range dropped {
		switch entry.Role {
		case "assistant":
			for _, id := range toolCallIDs(entry) {
				calls[id] = append(calls[id], i)
			}
		case "tool":
			if entry.ToolCallID != "" {
				results[entry.ToolCallID] = append(results[entry.ToolCallID], i)
			}
		}
	}

	keep := make([]bool, len(dropped))
	for i, entry := range dropped {
		if !flaggedIDs[entry.ID] {
			continue
		}
		keep[i] = true
		if entry.Role == "tool" {
			for _, ci := range calls[entry.ToolCallID] {
				keep[ci] = true
			}
		}
		if entry.Role == "assistant" {
			for _, id := range toolCallIDs(entry) {
				for _, ri := range results[id] {
					keep[ri] = true
				}
			}
		}
	}

	out := make([]Record, 0, len(dropped))
	for i, keepIt := range keep {
		if keepIt {
			out = append(out, Record{Kind: entryKind(dropped[i]), Data: dropped[i].Data})
		}
	}
	return out
}

// entryKind 将对话角色映射回用于重新发出该记录的记录 kind，
// 以便 messageEntry 将恢复的记录投影为相同的 Entry。
func entryKind(entry Entry) string {
	switch entry.Role {
	case "user":
		return KindUser
	case "assistant":
		return KindAssistant
	case "tool":
		return KindTool
	case "system":
		return KindSummary
	default:
		return KindMessage
	}
}

// CutForBudget 返回对话中应保留的第一个索引，使保留的后缀能够容纳在预算内。
// 返回零表示没有值得压缩的内容：整个历史已经放得下，或者预算太小，连一对消息都放不下。
func CutForBudget(entries []Entry, budget int) int {
	keep := KeepForBudget(entries, budget)
	if keep <= 0 || keep >= len(entries) {
		return 0
	}
	return adjustCut(entries, len(entries)-keep)
}

// KeepForBudget 报告有多少末尾条目能够容纳在预算内。它从最新的条目向前遍历，
// 因此最近的上下文得以保留。
func KeepForBudget(entries []Entry, budget int) int {
	if budget <= 0 {
		return 0
	}
	total, keep := 0, 0
	for i := len(entries) - 1; i >= 0; i-- {
		cost := estimateEntryTokens(entries[i])
		if total+cost > budget {
			break
		}
		total += cost
		keep++
	}
	return keep
}

// adjustCut 使提议的切分点对工具配对安全：工具调用及其结果必须落在切分点的同一侧。
// 它首先保留原有行为，即向前回退以跳过末尾连续的工具消息，然后利用 assistant 载荷和工具结果中
// 携带的工具调用 id，继续回退以跳过任何横跨切分点的调用/结果对。回退会逐字保留更多消息
// （该对移入保留的后缀）；它从不将切分点向前移动，因此绝不会出现调用被摘要而其结果被保留
// （或反之）的情况。Compact(keep) 和 CompactBudget 都使用这个辅助函数。
func adjustCut(entries []Entry, start int) int {
	// 遗留的无 id 情况：绝不在末尾连续的工具结果中间切分。
	for start > 0 && start < len(entries) && entries[start].Role == "tool" {
		start--
	}

	// 将每个工具调用 id 映射到声明它的 assistant 索引。转录中可能包含使用相同 id 的重试，
	// 因此保留所有位置。
	callIndices := make(map[string][]int)
	for i, entry := range entries {
		if entry.Role != "assistant" {
			continue
		}
		for _, id := range toolCallIDs(entry) {
			callIndices[id] = append(callIndices[id], i)
		}
	}

	// 对于每一对（调用、结果），切分点不得严格落在两者之间，即不能落在 (call, result] 内。
	// 将 start 降低到调用的索引即可解决这种拆分；重复此过程，因为新的 start 可能位于另一对的范围之内。
	for {
		moved := false
		for i, entry := range entries {
			if entry.Role != "tool" || entry.ToolCallID == "" {
				continue
			}
			call := lastCallBefore(callIndices[entry.ToolCallID], i)
			if call >= 0 && call < start && start <= i {
				start = call
				moved = true
				break
			}
		}
		if !moved {
			break
		}
	}
	return start
}

// toolCallIDs 提取 assistant 条目声明的工具调用 id。assistant 记录持久化完整的 llm.Message 载荷，
// 其键为 "tool_calls"（某些生产者使用 "toolCalls"）；较旧的记录可能只携带单个 "tool_call_id"。
// 没有任何 id 的条目不产生配对，从而保留 adjustCut 的无 id 末尾连续段行为。
func toolCallIDs(entry Entry) []string {
	if len(entry.Data) == 0 {
		if entry.ToolCallID != "" {
			return []string{entry.ToolCallID}
		}
		return nil
	}
	var payload struct {
		Snake []struct {
			ID string `json:"id"`
		} `json:"tool_calls"`
		Camel []struct {
			ID string `json:"id"`
		} `json:"toolCalls"`
	}
	if err := json.Unmarshal(entry.Data, &payload); err != nil {
		if entry.ToolCallID != "" {
			return []string{entry.ToolCallID}
		}
		return nil
	}
	ids := make([]string, 0, len(payload.Snake)+len(payload.Camel))
	for _, call := range payload.Snake {
		if call.ID != "" {
			ids = append(ids, call.ID)
		}
	}
	for _, call := range payload.Camel {
		if call.ID != "" {
			ids = append(ids, call.ID)
		}
	}
	if len(ids) == 0 && entry.ToolCallID != "" {
		ids = append(ids, entry.ToolCallID)
	}
	return ids
}

// lastCallBefore 返回严格位于 before 之前的最大调用索引；如果该 id 在转录的这一侧没有调用，则返回 -1。
func lastCallBefore(indices []int, before int) int {
	last := -1
	for _, index := range indices {
		if index >= before {
			break
		}
		last = index
	}
	return last
}

// estimateEntryTokens 是仅用于压缩切分点的保守、无分配估算。宽字符（CJK）每个算一个 token；
// 其他字符遵循通常的每四字符一个 token 规则。
func estimateEntryTokens(entry Entry) int {
	cost := 4 + estimateRunes(entry.Content) + estimateRunes(entry.Name) + estimateRunes(entry.ToolCallID)
	cost += len(entry.Data) / 4
	return cost
}

func estimateRunes(text string) int {
	wide, narrow := 0, 0
	for _, r := range text {
		if (r >= 0x2E80 && r <= 0xA4CF) || (r >= 0xAC00 && r <= 0xD7A3) ||
			(r >= 0xF900 && r <= 0xFAFF) || (r >= 0xFE30 && r <= 0xFE6F) ||
			(r >= 0xFF00 && r <= 0xFF60) || (r >= 0x20000 && r <= 0x3FFFD) {
			wide++
		} else {
			narrow++
		}
	}
	return wide + (narrow+3)/4
}

// summarySchemaEnv 是压缩摘要固定 schema 补全的 opt-in 环境变量。只有值恰为 "1"
// 时 Summarize 才在产出摘要后应用 schema；默认关闭，保证既有压缩记录的字节逐字不变。
const summarySchemaEnv = "ARGENT_SUMMARY_SCHEMA"

// summarySchemaEnabled 报告当前进程是否开启了压缩摘要 schema 补全。
// 它在每次调用时读取环境变量，便于测试用 t.Setenv 精确控制，且不引入可变全局状态。
func summarySchemaEnabled() bool {
	return strings.TrimSpace(os.Getenv(summarySchemaEnv)) == "1"
}

// ApplySummarySchema 把 R36 交付的两个原语接到一起，作用于一段已生成的摘要：
//   - 先调用 SummaryIsEmpty 检测摘要是否无实质内容，结果作为第二个返回值返回，
//     供上层据此决定是否重试模型摘要；
//   - 再调用 EnsureSummarySections 按固定顺序补齐缺失的 Goal/State/Next/Files 小节，
//     Files 段内容从 files（已记录的改动文件事件）机械生成。
//
// 本函数不读取全局状态，也不修改 files 入参；opt-in 的判定由 Summarize 负责。
func ApplySummarySchema(summary string, files []string) (string, bool) {
	empty := SummaryIsEmpty(summary)
	// files 来自 Summarize 的 pathsFromData：它们是工具事件载荷中已记录的改动文件。
	// 这些文件都视为编辑操作；没有可用行号/命令文本，故 Lines 留空。
	ops := make([]FileOp, 0, len(files))
	for _, path := range files {
		if path = strings.TrimSpace(path); path != "" {
			ops = append(ops, FileOp{Path: path, Edited: true})
		}
	}
	return EnsureSummarySections(summary, ops), empty
}

// Summarize 将被丢弃的条目转换为压缩载荷，保留后续轮次所需的结构：目标、涉及的文件、失败和下一步。
func Summarize(dropped []Entry, keep int) Compaction {
	var goals, files, failures, next []string
	seenFiles := make(map[string]bool)
	seenGoals := make(map[string]bool)
	seenFailures := make(map[string]bool)
	seenNext := make(map[string]bool)

	// 所有来源（当前窗口的启发式 + 旧压缩记录的权威字段）都经过这些回调归一并去重。
	// 去重是必要的：rebuildCompactionSummary/enrichCompaction 会把 Files/Failures/NextSteps
	// 原样写成项目符号，若旧压缩的同一条目既来自权威字段又来自摘要文本，重建后就会出现重复符号。
	// stripBulletPrefix 进一步把"从旧摘要文本重新提取的项目符号"与"权威字段原文"对齐，
	// 例如 "- Next steps: X" 与 "Next steps: X" 应视为同一条而非两条。
	addGoal := func(text string) {
		if text = strings.TrimSpace(stripBulletPrefix(text)); text != "" && !seenGoals[text] {
			seenGoals[text] = true
			goals = append(goals, text)
		}
	}
	addFailure := func(text string) {
		if text = strings.TrimSpace(stripBulletPrefix(text)); text != "" && !seenFailures[text] {
			seenFailures[text] = true
			failures = append(failures, text)
		}
	}
	addNext := func(text string) {
		if text = strings.TrimSpace(stripBulletPrefix(text)); text != "" && !seenNext[text] {
			seenNext[text] = true
			next = append(next, text)
		}
	}
	addFile := func(path string) {
		if path = strings.TrimSpace(path); path != "" && !seenFiles[path] {
			seenFiles[path] = true
			files = append(files, path)
		}
	}

	for _, entry := range dropped {
		switch entry.Role {
		case "user":
			if goal := firstLine(entry.Content, 200); goal != "" {
				addGoal(goal)
			}
		case "tool":
			if reason, ok := failureReason(entry.Content); ok {
				addFailure(failureBullet(entry.Name, reason))
			}
		}
		if entry.Role == "assistant" || entry.Role == "system" {
			for _, step := range nextStepLines(entry.Content, 200) {
				addNext(step)
			}
		}
		for _, path := range pathsFromData(entry.Data) {
			addFile(path)
		}
		// 多次压缩链中，旧压缩记录以 system Entry 形式重新进入 Messages() 投影：
		// 它的权威 Files/Failures/NextSteps 在 Data 里，原始目标在 Summary 的 ## Goal 段。
		// 若只靠上面的启发式，第二次压缩会丢失 Goal/Failures/NextSteps，只剩 Files。
		if entry.Role == "system" {
			mergePriorCompaction(entry, addGoal, addFailure, addNext, addFile)
		}
	}
	// files 按时间顺序收集；在排序前保留最近十二个，否则"最近"就会变成字典序最后的那些。
	files = tailItems(files, 12)
	sort.Strings(files)
	// 原始目标必须始终留在 Goal 段：steering、预算提醒、纠错引导都以 Role:"user"
	// 追加，若只保留末五条，窗口内最早的目标会被这些合成轮挤出。
	goals = keepFirstAndTail(goals, 5)
	failures = tailItems(failures, 8)
	next = tailItems(next, 5)

	var summary strings.Builder
	summary.WriteString("## Compaction\n")
	fmt.Fprintf(&summary, "- dropped %d message(s), kept the last %d\n", len(dropped), keep)
	writeSection(&summary, "Goal", goals)
	writeSection(&summary, "Files changed", files)
	writeSection(&summary, "Failures", failures)
	writeSection(&summary, "Next steps", next)
	// 对被丢弃的读取/编辑/命令进行确定性的、无需 LLM 的记录，
	// 使恢复的轮次不会重复这些操作或遗忘某项改动。
	if activity := summarizeToolActivity(dropped); activity != "" {
		summary.WriteString(activity)
	}

	compaction := Compaction{
		Summary:   summary.String(),
		Dropped:   len(dropped),
		Keep:      keep,
		Files:     files,
		Failures:  failures,
		NextSteps: next,
	}
	// R36 opt-in：仅在 ARGENT_SUMMARY_SCHEMA=1 时触碰摘要；默认关闭时上面的字节
	// 原样返回，与旧版逐字一致。补缺的小节与空摘要标记都由 ApplySummarySchema 完成。
	if summarySchemaEnabled() {
		compaction.Summary, compaction.SummaryEmpty = ApplySummarySchema(compaction.Summary, files)
	}
	return compaction
}

// mergePriorCompaction 从 system Entry 的 Data 解出旧 Compaction，把其权威字段合并进
// 当前压缩载荷。它只读取 entry.Data，不修改任何入参；Data 无法解为 Compaction 时静默跳过，
// 因为系统消息并不一定都是压缩记录。
//
// 目标只从 Summary 的 ## Goal 段提取：Compaction.Structured().Progress 混合了目标、文件与
// 工具活动，不适合作为目标来源；ParseSummarySections 接受中英文别名并精确定位章节边界。
// Failures/NextSteps 同时合并权威字段与结构化投影，二者经 add* 回调按值去重后不会重复。
func mergePriorCompaction(entry Entry, addGoal, addFailure, addNext, addFile func(string)) {
	if len(entry.Data) == 0 {
		return
	}
	var prior Compaction
	if err := json.Unmarshal(entry.Data, &prior); err != nil {
		return
	}
	if sections := ParseSummarySections(prior.Summary); sections.HasGoal {
		for _, line := range strings.Split(sections.Goal, "\n") {
			if item, ok := summaryBullet(strings.TrimSpace(line)); ok {
				addGoal(truncateRunes(item, 200))
			}
		}
	}
	for _, f := range prior.Failures {
		addFailure(f)
	}
	for _, n := range prior.NextSteps {
		addNext(n)
	}
	for _, f := range prior.Files {
		addFile(f)
	}
	// 结构化投影覆盖"文本里有但字段里没有"的旧记录；与权威字段来源重叠时由 add* 去重。
	if structured, ok := prior.Structured(); ok {
		for _, f := range structured.Uncertainties {
			addFailure(f)
		}
		for _, n := range structured.UserActions {
			addNext(n)
		}
	}
}

func writeSection(builder *strings.Builder, title string, items []string) {
	fmt.Fprintf(builder, "\n## %s\n", title)
	if len(items) == 0 {
		builder.WriteString("- (none)\n")
		return
	}
	for _, item := range items {
		fmt.Fprintf(builder, "- %s\n", item)
	}
}

// stripBulletPrefix 去掉 markdown 项目符号前缀，使"从旧摘要文本重新提取的条目"与
// "压缩载荷里的权威字段原文"归一为同一文本，供 Summarize 的 seen* 集合去重。
// writeSection 会在写回时重新添加统一的 "- " 前缀，因此这里去掉前缀不会丢失结构。
func stripBulletPrefix(text string) string {
	for _, prefix := range []string{"- ", "* ", "• "} {
		if strings.HasPrefix(text, prefix) {
			return text[len(prefix):]
		}
	}
	return text
}

// nextStepMarkers 是模型用来记录未完成工作的简短显式提示。匹配故意宽松（子串、忽略大小写）：
// 误报只会在摘要中多出一个项目符号，而漏报则会丢失恢复运行所需的计划。
var nextStepMarkers = []string{
	"next step", "next:", "todo", "follow up", "follow-up", "remaining", "下一步", "后续", "待办",
}

// nextStepLines 只保留真正带有下一步标记的行。计划通常写在摘要行之后，
// 因此只检查条目的第一行会漏掉它。
func nextStepLines(content string, limit int) []string {
	var out []string
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// 上一次压缩的摘要会被投影为 system Entry，并在后续压缩丢弃它时再次送入 Summarize。
		// 它自身的 markdown 章节标题（"## Next steps"、"# TODO" 等）包含下面的标记，
		// 但那是结构而非计划：匹配它们会把标题变成新摘要中类似 "- ## Next steps" 的项目符号。
		// 标题行从不包含可执行的下一步，因此跳过它们。
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		lower := strings.ToLower(trimmed)
		marked := false
		for _, marker := range nextStepMarkers {
			if strings.Contains(lower, marker) {
				marked = true
				break
			}
		}
		if !marked {
			continue
		}
		if step := truncateRunes(trimmed, limit); step != "" {
			out = append(out, step)
		}
	}
	return out
}

// tailItems 保留最近的 limit 个条目。
func tailItems(items []string, limit int) []string {
	if limit <= 0 || len(items) <= limit {
		return items
	}
	return items[len(items)-limit:]
}

// keepFirstAndTail 保留首项与末 limit-1 项。首项通常是压缩窗口内最早的原始目标，
// 而 steering、预算提醒、纠错引导等合成消息同样以 Role:"user" 追加；
// 若只保留末 limit 项，原始目标会被这些合成轮挤出 Goal 段。
// 首项若与尾部某项文本相同则去重，避免产生重复的项目符号。
// 仅对字符串切片做取舍，不切分字符串本身，因此不会破坏 UTF-8 字素边界。
func keepFirstAndTail(items []string, limit int) []string {
	if limit <= 0 || len(items) <= limit {
		return items
	}
	first := items[0]
	tail := items[len(items)-(limit-1):]
	out := make([]string, 0, limit)
	out = append(out, first)
	for _, item := range tail {
		if item == first {
			continue
		}
		out = append(out, item)
	}
	return out
}

// lowerByteOffset 将 strings.ToLower(s) 中的字节偏移映射回 s 中对应的字节偏移。
// strings.ToLower 按符文逐一折叠，因此即使某个符文的小写形式字节长度不同（例如 U+0130 2->1、
// U+1E9E 3->2、U+212A 3->1 字节），两个字符串中的符文索引也相同。
// 否则按小写后的字节偏移切分原字符串会从符文中间开始并破坏文本——snippetFor 也使用同样的对齐规则。
func lowerByteOffset(s, lowered string, offset int) int {
	if offset <= 0 {
		return 0
	}
	runes := utf8.RuneCountInString(lowered[:offset])
	idx := 0
	for n := 0; n < runes; n++ {
		_, size := utf8.DecodeRuneInString(s[idx:])
		if size == 0 {
			break
		}
		idx += size
	}
	return idx
}

// failureReason 识别表示失败的工具结果。
func failureReason(content string) (string, bool) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return "", false
	}
	lower := strings.ToLower(trimmed)
	if index := strings.LastIndex(lower, "[exit "); index >= 0 {
		// lower 是在忽略大小写的情况下搜索的，因此原始文本中的标记可能写作 "[EXIT " 或 "[Exit "。
		// 应从 lower 读取数字（ASCII 数字不受大小写折叠影响），而不是从 tail 中剥离仅小写的
		// 前缀；后者会静默丢失所有 shell 以其他大小写打印标记的失败。
		rest := lower[index+len("[exit "):]
		code := 0
		digits := 0
		for _, ch := range rest {
			if ch < '0' || ch > '9' {
				break
			}
			code = code*10 + int(ch-'0')
			digits++
		}
		if digits > 0 && code != 0 {
			// index 是 lower 中的字节偏移；在切分前将其映射回 trimmed，
			// 这样前面某个小写形式更短的符文就不会把窗口推移到码点中间（或丢失失败信息）。
			tail := trimmed[lowerByteOffset(trimmed, lower, index):]
			return firstLine(tail, 160), true
		}
	}
	if strings.Contains(lower, "timed out") {
		return "timed out", true
	}
	if reason := failureLine(trimmed, 160); reason != "" {
		return reason, true
	}
	return "", false
}

// failureMarkers 是识别失败工具结果的提示。匹配是在单行上进行的忽略大小写子串匹配，
// 因此即使失败被埋在进度输出（"compiling..."、"building"）之后也能被捕获。
var failureMarkers = []string{
	"error", "failed", "panic", "traceback", "fatal", "错误", "失败", "异常", "崩溃",
}

// failureLine 返回内容中第一行报告失败的文本。它扫描整个结果而非仅第一行，
// 然后将出错的行裁剪到 limit 个符文。
func failureLine(content string, limit int) string {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		for _, marker := range failureMarkers {
			if strings.Contains(lower, marker) {
				return truncateRunes(trimmed, limit)
			}
		}
	}
	return ""
}

func failureBullet(name, reason string) string {
	if strings.TrimSpace(name) == "" {
		name = "tool"
	}
	return name + " — " + reason
}

// pathsFromData 收集记录载荷中提到的文件路径，包括本身就是 JSON 字符串的工具调用参数。
func pathsFromData(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	var out []string
	seen := make(map[string]bool)
	collectPaths(value, &out, seen)
	return out
}

func collectPaths(value any, out *[]string, seen map[string]bool) {
	switch typed := value.(type) {
	case map[string]any:
		// JSON 对象解码为 Go map，其迭代顺序是随机的。pathsFromData 为 Summarize 提供数据，
		// 后者在排序前只保留最近的十二条路径（见 tailItems(files, 12)）。如果截断点恰好落在
		// 某个携带多条路径的载荷内部，map 的顺序就会决定哪些子集得以保留——同一份转录
		// 每次运行都可能产生不同的 "Files changed" 部分。按排序后的顺序访问键，
		// 使出现顺序具有确定性；search.go 中的 collectStrings 也采用同样的规则。
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			child := typed[key]
			switch key {
			case "path", "file":
				if text, ok := child.(string); ok {
					addPath(out, seen, text)
				}
			case "files":
				switch list := child.(type) {
				case []any:
					for _, item := range list {
						if text, ok := item.(string); ok {
							addPath(out, seen, text)
						}
					}
				case string:
					addPath(out, seen, list)
				}
			case "arguments", "args", "input":
				if text, ok := child.(string); ok {
					var inner any
					if json.Unmarshal([]byte(text), &inner) == nil {
						collectPaths(inner, out, seen)
					}
					continue
				}
				collectPaths(child, out, seen)
			default:
				collectPaths(child, out, seen)
			}
		}
	case []any:
		for _, item := range typed {
			collectPaths(item, out, seen)
		}
	}
}

func addPath(out *[]string, seen map[string]bool, path string) {
	path = strings.TrimSpace(path)
	if path == "" || seen[path] {
		return
	}
	seen[path] = true
	*out = append(*out, path)
}

// ---------------------------------------------------------------------------
// 无损压缩归档。
//
// 压缩只添加一条摘要记录；原始 JSONL 转录从不被重写，因此"无损"在文件层面已经成立。
// 这些辅助函数补上了 gptme 审计项的后半部分：为模型友好、可按字节寻址的被丢弃消息副本。
// 它们被刻意设计为纯函数（不接入 Compact/appendCompaction），
// 因此现有压缩行为保持逐字节一致。归档存放在 <session-store-dir>/compactions/ 下，
// Container.List 已跳过该子目录，因为它只考虑顶层的 *.jsonl 文件。
// 不会在 .argent/ 下写入任何内容。
// ---------------------------------------------------------------------------

const compactionArchiveDir = "compactions"

// compactionArchiveID 以与 Container.path 验证会话 id 相同的方式验证归档 id，
// 确保 id 永远不会逃出 compactions 目录。为方便起见，结尾的 ".jsonl" 会被接受并剥离。
func compactionArchiveID(id string) (string, error) {
	clean := strings.TrimSpace(id)
	if strings.HasSuffix(strings.ToLower(clean), fileExt) {
		clean = clean[:len(clean)-len(fileExt)]
	}
	if clean == "" || clean == "." || clean == ".." ||
		strings.ContainsAny(clean, `/\:*?"<>|`) {
		return "", fmt.Errorf("session: invalid compaction archive id %q", id)
	}
	return clean, nil
}

// CompactionArchivePath 返回 id 对应归档在磁盘上的路径：<dir>/compactions/<id>.jsonl。
// id 会经过路径安全性校验。
func CompactionArchivePath(dir, id string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", fmt.Errorf("session: empty compaction archive directory")
	}
	clean, err := compactionArchiveID(id)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, compactionArchiveDir, clean+fileExt), nil
}

// compactionArchiveRelPath 是存储在 ArchiveRef 中的路径：相对于会话存储目录，
// 使用正斜杠，以便在任何操作系统上生成稳定的记录。
func compactionArchiveRelPath(id string) string {
	return filepath.ToSlash(filepath.Join(compactionArchiveDir, id+fileExt))
}

// writeCompactionArchive 是共享实现。它以原子方式（临时文件 + 重命名）将消息写入 JSONL，
// 为每条消息计算字节区间，并返回绝对路径和一个 Path 相对于 dir 的 ArchiveRef。
// 任何错误路径都不会留下临时文件。
func writeCompactionArchive(dir, id string, messages []Entry) (path string, ref *ArchiveRef, err error) {
	full, err := CompactionArchivePath(dir, id)
	if err != nil {
		return "", nil, err
	}
	clean, _ := compactionArchiveID(id) // 已由 CompactionArchivePath 校验

	archiveDir := filepath.Dir(full)
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		return "", nil, fmt.Errorf("session: create %s: %w", archiveDir, err)
	}

	tmp, err := os.CreateTemp(archiveDir, ".compact-*.tmp")
	if err != nil {
		return "", nil, fmt.Errorf("session: archive temp in %s: %w", archiveDir, err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // 保持原始文本逐字节忠实（不产生 \u003c 等转义）

	ranges := make([]ArchiveRange, 0, len(messages))
	var offset int64
	for _, entry := range messages {
		buf.Reset()
		if err := enc.Encode(entry); err != nil {
			return "", nil, fmt.Errorf("session: encode archived message %q: %w", entry.ID, err)
		}
		start := offset
		n, err := tmp.Write(buf.Bytes())
		if err != nil {
			return "", nil, fmt.Errorf("session: write archive %s: %w", full, err)
		}
		offset += int64(n)
		ranges = append(ranges, ArchiveRange{
			ID:    entry.ID,
			Role:  entry.Role,
			Start: start,
			End:   offset,
		})
	}
	if err := tmp.Close(); err != nil {
		return "", nil, fmt.Errorf("session: close archive %s: %w", full, err)
	}
	// os.Rename 在 Windows 上不会覆盖已存在的文件，因此先删除同一 id 的旧归档。
	// id 预期在每次压缩中都是唯一的；只有在调用方刻意重写时此处理才有意义。
	if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
		return "", nil, fmt.Errorf("session: replace archive %s: %w", full, err)
	}
	if err := os.Rename(tmpName, full); err != nil {
		return "", nil, fmt.Errorf("session: rename archive to %s: %w", full, err)
	}
	committed = true

	return full, &ArchiveRef{
		Path:   compactionArchiveRelPath(clean),
		Bytes:  offset,
		Ranges: ranges,
	}, nil
}

// WriteCompactionArchive 将消息写入无损归档 <dir>/compactions/<id>.jsonl 并返回磁盘路径。
// 写入是原子性的，不会留下临时文件。
func WriteCompactionArchive(dir, id string, messages []Entry) (path string, err error) {
	path, _, err = writeCompactionArchive(dir, id, messages)
	return path, err
}

// WriteCompactionArchiveRef 在 WriteCompactionArchive 的基础上，额外返回在 Compaction 记录中
// 嵌入引用所需的字节区间和相对于存储目录的路径。
func WriteCompactionArchiveRef(dir, id string, messages []Entry) (*ArchiveRef, error) {
	_, ref, err := writeCompactionArchive(dir, id, messages)
	return ref, err
}

// resolveArchivePath 将存储的 ref.Path 转换为文件系统路径。绝对路径原样使用；
// 相对路径在 dir 下解析。使用正斜杠的 ref.Path（由 compactionArchiveRelPath 写入）
// 在 Windows 上同样有效。
func resolveArchivePath(dir, path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", fmt.Errorf("session: empty compaction archive path")
	}
	candidate := trimmed
	if !filepath.IsAbs(candidate) {
		// 将存储的相对于存储目录的路径（正斜杠）规范化为当前操作系统的格式。
		candidate = filepath.Join(dir, filepath.FromSlash(candidate))
	}
	// 该路径可能来自持久化记录，因此绝不能让它逃出存储目录
	// （与处理其他所有文件系统路径的不变量一致）。
	base, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("session: resolve archive dir %s: %w", dir, err)
	}
	full, err := filepath.Abs(candidate)
	if err != nil {
		return "", fmt.Errorf("session: resolve archive path %s: %w", path, err)
	}
	rel, err := filepath.Rel(base, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("session: compaction archive path %q escapes store dir", path)
	}
	return full, nil
}

// ReadCompactionArchive 将归档读回为消息，保留每个字段（包括原始的 json.RawMessage Data）。
// 空白行和格式错误的行会被跳过，与 readRecords 的宽松策略一致。path 可以是
// WriteCompactionArchive 返回的绝对路径，也可以是 ArchiveRef 中存储的相对于存储目录的 Path。
func ReadCompactionArchive(dir, path string) ([]Entry, error) {
	full, err := resolveArchivePath(dir, path)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(full)
	if err != nil {
		return nil, fmt.Errorf("session: read archive %s: %w", full, err)
	}
	defer file.Close()

	entries := make([]Entry, 0, 16)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineLen)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var entry Entry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return entries, fmt.Errorf("session: read archive %s: %w", full, err)
	}
	return entries, nil
}

// ReadArchiveRange 从归档中精确读取一个字节区间，这是将 Compaction 记录中的 ArchiveRange
// 还原为原始消息字节的基础操作。边界会对照文件进行检查，
// 因此损坏的偏移量无法读取到归档之外。
func ReadArchiveRange(dir, path string, rng ArchiveRange) ([]byte, error) {
	full, err := resolveArchivePath(dir, path)
	if err != nil {
		return nil, err
	}
	if rng.Start < 0 || rng.End < rng.Start {
		return nil, fmt.Errorf("session: invalid archive range [%d,%d)", rng.Start, rng.End)
	}
	info, err := os.Stat(full)
	if err != nil {
		return nil, fmt.Errorf("session: stat archive %s: %w", full, err)
	}
	if rng.End > info.Size() {
		return nil, fmt.Errorf("session: archive range [%d,%d) exceeds file size %d", rng.Start, rng.End, info.Size())
	}
	file, err := os.Open(full)
	if err != nil {
		return nil, fmt.Errorf("session: read archive %s: %w", full, err)
	}
	defer file.Close()

	buf := make([]byte, rng.End-rng.Start)
	if _, err := file.ReadAt(buf, rng.Start); err != nil {
		return nil, fmt.Errorf("session: read archive range [%d,%d): %w", rng.Start, rng.End, err)
	}
	return buf, nil
}

// CompactionArchiveNotice 生成一段简短、模型可读的引用文本，可附加到压缩摘要/通知中，
// 使恢复的轮次知道确切的原文是可恢复的以及如何寻址它。它是纯辅助函数：
// 除非调用方显式选择启用，否则压缩不会调用它。
func CompactionArchiveNotice(ref *ArchiveRef) string {
	if ref == nil || strings.TrimSpace(ref.Path) == "" {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n## Lossless archive\n")
	fmt.Fprintf(&b, "- The verbatim text of the dropped messages is preserved in `%s` (%d bytes).\n", ref.Path, ref.Bytes)
	fmt.Fprintf(&b, "- To recover one message, read the byte range `start..end` for its index from the `archive.ranges` field of this compaction record.\n")
	return b.String()
}
