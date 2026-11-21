package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/provider"
	"github.com/2118505078xyh1/argentgo/internal/session"
)

const (
	// defaultAutoCompactTokens 对应 agent 循环的上下文目标：达到该大小的对话很适合生成一条摘要记录。
	defaultAutoCompactTokens = 32000
	// defaultCompactKeep 在压缩后原样保留最近的轮次。
	defaultCompactKeep = 6
)

// compactThresholdFrom 从合并配置中读取可选的嵌套块 {"compaction":{"threshold":N}}，
// 使触发阈值无需标志即可配置。
func compactThresholdFrom(raw map[string]any) int {
	if nested, ok := raw["compaction"].(map[string]any); ok {
		if value := intField(nested, "threshold", "tokens", "autoCompactTokens"); value > 0 {
			return value
		}
	}
	return 0
}

// sessionCompactor 是 agent.Compactor 在 app 侧的实现。它拥有会话，
// 因此 agent 包保持不依赖任何 session。
type sessionCompactor struct {
	res  Resolved
	sess *session.Session
	reg  *OptInRegistry

	// 模型摘要（opt-in model_summary）所需的运行时依赖。ctx/chatter/summaryModel/
	// emit 都由 Execute 在构造后注入；零值时 wire 完全跳过，逐字节不变。
	ctx          context.Context
	chatter      llm.Chatter
	summaryModel string
	emit         func(agent.Event)
}

func newSessionCompactor(res Resolved, sess *session.Session) *sessionCompactor {
	return &sessionCompactor{res: res, sess: sess}
}

// newSessionCompactorWithOpts 是带 opt-in 注册表的构造，仅供 Execute 使用。
// 原两参构造保留给测试，reg 留 nil 时三个压缩 wire 全部关闭，重建路径逐字节不变。
func newSessionCompactorWithOpts(res Resolved, sess *session.Session, reg *OptInRegistry) *sessionCompactor {
	return &sessionCompactor{res: res, sess: sess, reg: reg}
}

// compactionInputs 解析驱动压缩触发的三元组 (contextWindow, configuredTokens, percent)。
// 不知道上下文窗口时会忽略百分比，使触发阈值回退到配置的 token 数或编译默认值，
// 而不是坍缩为 1。
func compactionInputs(res Resolved) (window, configured, percent int) {
	window = res.Cfg.ContextWindow
	// 未显式配置窗口时按模型元数据推导（claude 200k / gpt 128k+ / gemini 1M 等）。
	// 推导只影响"配置了 condensePercent"的百分比阈值；percent 为 0 时 window 不参与
	// 决策，因此这个回落对未配置百分比的运行逐字节不变。未知模型返回 0，维持原回落。
	if window <= 0 {
		window = provider.ContextWindowFor(res.Provider, res.Model, 0)
	}
	configured = res.AutoCompactTokens
	percent = res.Cfg.CondensePercent(res.Profile)
	if window <= 0 {
		percent = 0
	}
	return
}

// compactionDecision 是各压缩路径共享的纯比较逻辑：估算值与触发阈值对比。
// 把它拆出来，使上下文窗口百分比计算和 1.3 倍膨胀可以用桩估算值测试，
// 而无需调用模型或会话。
func compactionDecision(estimate, window, configured, percent int) (threshold int, compact bool) {
	threshold = agent.CompactionThreshold(window, configured, percent)
	if threshold <= 0 {
		return threshold, false
	}
	return threshold, agent.InflateEstimate(estimate) >= threshold
}

// minCompactionSavingsDivisor 是 gptme 的 MIN_SAVINGS_RATIO 下限，表示为
// 1/minCompactionSavingsDivisor（十分之一，即 10%）：一次压缩若移除的上下文不足该比例，
// 就不值得造成提示缓存失效，因此决策路径会让对话保持原样。
const minCompactionSavingsDivisor = 10

// compactionWorthwhile 报告把 `before` 个估算 token 的上下文换成 `after` 个的上下文，
// 节省的量是否足以证明破坏提示缓存是值得的。比较为 (before-after)/before >= 10%；
// 它使用带向上取整的整数运算，以确定且不会溢出。非正的 `before` 没有比例，绝不值得压缩；
// `after` 若不是严格小于 `before`，则是净亏损。
func compactionWorthwhile(before, after int64) bool {
	if before <= 0 || after >= before {
		return false
	}
	// ceil(before / minCompactionSavingsDivisor)，计算时不使用可能溢出的乘法。
	required := before / minCompactionSavingsDivisor
	if before%minCompactionSavingsDivisor != 0 {
		required++
	}
	return before-after >= required
}

// usageAnchor 是提供方在记录的某个位置报告的原生 token 数。Tokens 是助手回复的 totalTokens，
// Index 是该助手消息在消息切片中的位置；只有 Index 之后的消息才需要重新估算。
// 非正的 Tokens 或越界的 Index 表示没有可用的锚点，必须从头估算整段对话。
type usageAnchor struct {
	index  int
	tokens int
}

// valid 报告该锚点能否应用于长度为 n 的消息切片。
func (a usageAnchor) valid(n int) bool {
	return a.tokens > 0 && a.index >= 0 && a.index < n
}

// estimateContextTokens 估算下一次请求将发送的对话的 token 大小。有原生用量锚点时，
// 已测量的前缀原样使用，只有尾部的消息用字符数/4 + CJK 规则估算；没有锚点时估算整段对话。
// 在锚点之后重写上下文的压缩会使计数过期，因此也会强制完整重新估算。
func estimateContextTokens(messages []llm.Message, anchor usageAnchor) int {
	if anchor.valid(len(messages)) && !compactionAfter(messages, anchor.index) {
		return anchor.tokens + agent.EstimateMessages(messages[anchor.index+1:])
	}
	return agent.EstimateMessages(messages)
}

// compactionAfter 报告在锚点之后是否出现了压缩/上下文编辑边界。
// 位于锚点处或之前的边界已经反映在原生 totalTokens 中；之后的边界意味着被计数的上下文已被重写，
// 锚点已过期。
func compactionAfter(messages []llm.Message, from int) bool {
	for i := from + 1; i < len(messages); i++ {
		if isCompactionBoundary(messages[i]) {
			return true
		}
	}
	return false
}

// isCompactionBoundary 识别压缩记录所投影出的系统消息。会话路径（Session.Messages）
// 和轮次中路径（MaybeCompact）都以一条以 "## Compaction" 标题开头的系统消息发出摘要。
// 在受审计的设计中 "context_edit" 属于同一失效类别；本代码库只发出压缩这一种。
func isCompactionBoundary(m llm.Message) bool {
	return m.Role == "system" && strings.HasPrefix(strings.TrimSpace(m.Content), "## Compaction")
}

// assistantNativeUsage 读取助手记录的 JSON 负载中携带的提供方报告用量。
// agent 循环会序列化完整的回复消息（包括其 "usage" 对象），因此原生 totalTokens 会保留在记录中。
// 对于只有元数据或较旧的记录（没有可用锚点）返回 false。
func assistantNativeUsage(data []byte) (llm.Usage, bool) {
	if len(data) == 0 {
		return llm.Usage{}, false
	}
	var payload struct {
		Usage *llm.Usage `json:"usage"`
	}
	if err := json.Unmarshal(data, &payload); err != nil || payload.Usage == nil {
		return llm.Usage{}, false
	}
	if payload.Usage.TotalTokens() <= 0 {
		return llm.Usage{}, false
	}
	return *payload.Usage, true
}

// CompactThreshold 是 agent 用于轮次中压缩的触发阈值。负值配置会完全禁用自动压缩。
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

// ---------------------------------------------------------------------------
// 选择性启用的无损压缩归档接线。
//
// session 包提供了归档原语（WriteCompactionArchiveRef、Compaction.Archive、CompactionArchiveNotice），
// 但刻意不在自身的 Compact/appendCompaction 路径中调用它们，使现有压缩保持逐字节不变。
// app 层在这里选择启用：启用时，确切被丢弃的条目会写入 <session-dir>/compactions/ 下，
// 并把一个相对于存储的 ArchiveRef 附加到记录上。默认关闭。
// ---------------------------------------------------------------------------

// compactionArchiveEnabled 报告无损归档的选择性启用开关。可接受的配置形式
// （其他任何形式，包括缺少该键，都视为关闭）：
//
//	{"compaction":{"archive":true}}
//	{"archiveCompactions":true} / {"archive-compactions":true}
func compactionArchiveEnabled(raw map[string]any) bool {
	if nested, ok := raw["compaction"].(map[string]any); ok {
		if value, ok := nested["archive"].(bool); ok {
			return value
		}
	}
	for _, key := range []string{"archiveCompactions", "archive-compactions"} {
		if value, ok := raw[key].(bool); ok {
			return value
		}
	}
	return false
}

// nextCompactionArchiveID 生成路径安全的、按压缩划分的 id。会话 id 在创建会话时已被校验，
// "-cN" 后缀使同一会话中的连续压缩写入不同的文件，而不是相互覆盖。
func nextCompactionArchiveID(sess *session.Session) string {
	base := strings.TrimSpace(sess.ID())
	if base == "" {
		base = "session"
	}
	n := 1
	if records, err := sess.Records(); err == nil {
		for _, record := range records {
			if record.Kind == session.KindCompaction {
				n++
			}
		}
	}
	return fmt.Sprintf("%s-c%d", base, n)
}

// attachCompactionArchive 把被丢弃的条目写入无损归档，并把其引用以及一条模型可读的恢复通知
// 附加到压缩记录上。它返回磁盘路径（禁用时为空），使调用方和测试能断言文件存在。
// 错误会被返回而非吞掉：选择启用的调用方绝不能在没有它所要求的归档的情况下静默压缩。
func attachCompactionArchive(sess *session.Session, compaction *session.Compaction, dropped []session.Entry, res Resolved) (string, error) {
	if sess == nil || compaction == nil || !compactionArchiveEnabled(res.Cfg.Raw) {
		return "", nil
	}
	id := nextCompactionArchiveID(sess)
	ref, err := session.WriteCompactionArchiveRef(sess.Dir(), id, dropped)
	if err != nil {
		return "", err
	}
	compaction.Archive = ref
	compaction.Summary += session.CompactionArchiveNotice(ref)
	return session.CompactionArchivePath(sess.Dir(), id)
}

// MaybeCompact 把对话中较旧的部分折叠为一条会话压缩记录，并返回供下一次请求使用的折叠视图。
// 窗口是 agent 提供的 0.8 带目标；切点由该 token 预算推导，而非固定的消息数量。
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
	// 轮次中，运行的 OnCompactionEvidence 回调尚未触发，因此从可见消息中收集后续步骤行，
	// 并让 Summarize 从被丢弃的条目中提取工具失败记录。
	// dropped 与 reg 供 opt-in 压缩 wire 使用；reg 为 nil（测试/默认）时三 wire 关闭，逐字节不变。
	enrichCompaction(&compaction, agent.CollectCompactionEvidence(nil, messages), entries[:start], c.reg)

	out := make([]llm.Message, 0, len(messages)-start+2)
	out = append(out, llm.Message{Role: "system", Content: compaction.Summary})
	// 普通会话压缩会把原始系统提示一并折叠掉；这里显式保留它，
	// 使下一次模型请求仍能携带这些规则。
	if messages[0].Role == "system" {
		out = append(out, messages[0])
	}
	out = append(out, messages[start:]...)
	// 事后缩减检查（gptme hook.py）：如果折叠后的视图没有明显变小，
	// 压缩只会使提示缓存失效并丢失原文上下文。这种情况下让对话保持原样，
	// 且不写入压缩记录，使后续轮次能用更多历史再试。
	if !compactionWorthwhile(int64(agent.EstimateMessages(messages)), int64(agent.EstimateMessages(out))) {
		return messages, false
	}
	// 选择性启用的无损归档。只有在确定会追加压缩记录时才写入，
	// 这样被拒绝（不值得）的折叠不会留下孤儿归档。默认关闭时精确保持原有字节。
	if _, err := attachCompactionArchive(c.sess, &compaction, entries[:start], c.res); err != nil {
		return messages, false
	}
	// 该通知是摘要的一部分，因此把它镜像到下一次请求看到的折叠视图中。
	// 归档禁用时为空操作。
	// 模型摘要（opt-in）在归档之后、视图镜像之前应用，确保归档 notice 始终位于
	// 权威内容里；默认关闭时 applyModelSummaryWire 原样返回，逐字节不变。
	compaction.Summary = applyModelSummaryWire(compaction.Summary, c.reg, c.ctx, c.chatter,
		c.summaryModel, compaction.Failures, compaction.NextSteps, compaction.Files, c.emit)
	out[0].Content = compaction.Summary
	if err := c.sess.Append(session.KindCompaction, compaction); err != nil {
		return messages, false
	}
	return out, true
}

// entriesFromMessages 把 agent 的视图适配为会话压缩 API。重建的条目携带压缩器读取的字段：
// 角色、内容、结果上的工具调用 id，以及助手消息上的完整工具调用负载。
// 负载使用会话记录持久化的同一种形状——"tool_calls"，每次调用一个对象并带有 "id"——
// 使 adjustCut 能把调用与其结果配对，而不是静默丢弃 id。
// 工具参数中发现的文件目标会被规范化为 "files" 数组，会话路径扫描器已经能理解它。
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

// pathArgumentKeys 列出生产者用于表示文件目标的参数键。会话路径扫描器能识别
// "path"/"file"/"files"，但不认识 Claude 风格的 "file_path"/"filePath" 变体，
// 因此适配器把它们规范化为 "files" 数组，而不是在本包中重复实现那个扫描器。
var pathArgumentKeys = map[string]bool{
	"file_path": true, "filePath": true, "filepath": true,
	"file_name": true, "filename": true,
	"path": true, "file": true, "files": true,
}

// toolCallPaths 收集一批工具调用所声明的不同文件路径。参数是原始 JSON 字符串，
// 其本身可能还包含 JSON 字符串，因此会以递归方式访问嵌套对象。
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

// enrichCompaction 把运行时失败和后续步骤行带入摘要。
// 它对应 compactWithEvidence，使轮次中和运行后的压缩在恢复时保留相同的上下文。
func enrichCompaction(compaction *session.Compaction, evidence agent.CompactionEvidence, dropped []session.Entry, reg *OptInRegistry) {
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
	compaction.Summary = rebuildCompactionSummary(*compaction, dropped, orig, reg)
}

// maybeAutoCompact 是 --no-auto-compact 背后的运行后步骤。当该标志被设置，
// 或对话仍在阈值以内时，它是空操作。运行时失败和后续步骤行会被带入摘要，
// 使恢复的运行不会丢失它最需要的上下文。
func maybeAutoCompact(res Resolved, sess *session.Session, evidence agent.CompactionEvidence, reg *OptInRegistry, summaryDep *sessionCompactor) {
	if res.NoAutoCompact || sess == nil {
		return
	}
	if res.AutoCompactTokens < 0 {
		return
	}
	entries := sess.Messages()
	messages := make([]llm.Message, 0, len(entries))
	var anchor usageAnchor
	for i, entry := range entries {
		message := llm.Message{Role: entry.Role, Content: entry.Content}
		if entry.Role == "assistant" {
			if usage, ok := assistantNativeUsage(entry.Data); ok {
				message.Usage = &usage
				anchor = usageAnchor{index: i, tokens: usage.TotalTokens()}
			}
		}
		messages = append(messages, message)
	}
	window, configured, percent := compactionInputs(res)
	if _, compact := compactionDecision(estimateContextTokens(messages, anchor), window, configured, percent); !compact {
		return
	}
	hasEvidence := len(evidence.Failures) > 0 || len(evidence.NextSteps) > 0
	if hasEvidence {
		compactWithEvidence(sess, evidence, reg, summaryDep)
		return
	}
	_ = sess.Compact(defaultCompactKeep)
}
