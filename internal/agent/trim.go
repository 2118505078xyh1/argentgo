package agent

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/textutil"
	"github.com/2118505078xyh1/argentgo/internal/tokenize"
)

const (
	// messageOverheadTokens 涵盖每个提供方为每条记录添加的角色标记和消息框架。
	messageOverheadTokens = 4
	// trimmedPrefix 标记主体已被回收的工具结果。保留一行，使模型仍知道该调用发生过。
	trimmedPrefix = "[tool output trimmed:"
	// supersededStub 替换被同一文件较新一次读取所取代的旧读取。保留一个占位条目可维持工具调用/结果的配对。
	supersededStub = "[Superseded by a newer read of this file]"
	// uselessStub 在后续助手轮次消费了空搜索结果之后，将其替换掉。
	uselessStub = "[Uneventful result elided]"
	// cacheProtectSuffixTokens 是保守的提示缓存保护阈值：当某个结果之后仍有超过该数量的估算 token 时，
	// 把它替换成占位条目会使一大段缓存前缀失效，因此不动它。
	cacheProtectSuffixTokens = 12000

	// toolResultPruneThreshold 是 rune 数量阈值，超过它时工具结果的中间部分会被替换为标记，
	// 而不保留整个主体。
	toolResultPruneThreshold = 8192
	// toolResultPruneHead 和 toolResultPruneTail 是在裁剪超预算工具结果时，
	// 保留的开头和结尾 rune 数量。它们对应 dsh 的 compaction-tool-result-pruner 默认值。
	toolResultPruneHead = 4096
	toolResultPruneTail = 1024
	// toolResultPruneMarker 替换被移除的中间片段。其前后空行使保留的开头和结尾在视觉上与之分隔。
	toolResultPruneMarker = "\n\n[... tool result middle pruned ...]\n\n"
)

// EstimateTokens 估算一个字符串的 token 开销。宽 rune 表和字符数/4 规则位于 internal/tokenize，
// 使 agent、bash 体积提示和仓库映射共享同一份定义，而不是三份手工同步的副本。
func EstimateTokens(text string) int {
	return tokenize.Estimate(text)
}

// EstimateMessage 估算一条消息，包括其框架开销。
func EstimateMessage(m llm.Message) int {
	n := messageOverheadTokens + EstimateTokens(m.Content) + EstimateTokens(m.Reasoning)
	// 带图消息的 Content 为空、原始 part 数组在 ContentParts 旁路字段里。
	// opt-in 开启时把这部分计入，否则多模态会话会低估上下文压力并错过压缩时机。
	n += estimateMessageContentParts(m)
	for _, call := range m.ToolCalls {
		n += 2 + EstimateTokens(call.Function.Name) + EstimateTokens(call.Function.Arguments)
	}
	return n
}

// EstimateMessages 估算整段对话。
func EstimateMessages(messages []llm.Message) int {
	total := 0
	for _, m := range messages {
		total += EstimateMessage(m)
	}
	return total
}

// EstimateTools 估算随每次请求发送的工具 schema。
func EstimateTools(specs []llm.ToolSpec) int {
	total := 0
	for _, spec := range specs {
		total += messageOverheadTokens +
			EstimateTokens(spec.Function.Name) +
			EstimateTokens(spec.Function.Description) +
			EstimateTokens(string(spec.Function.Parameters))
	}
	return total
}

// TrimOptions 控制上下文裁剪。
type TrimOptions struct {
	// MaxTokens 是目标大小。零表示禁用裁剪。
	MaxTokens int
	// KeepTurns 是有多少末尾助手轮次保持原样。
	KeepTurns int
	// KeepToolTokens 是最近工具输出中保持原样的部分。较旧的输出会被一行占位条目替换。
	KeepToolTokens int
	// ToolMeta 把 ToolCallID 映射到工具运行器产生的元数据。非空时，会优先回收被取代的读取和无用的结果。
	ToolMeta map[string]ToolMeta
	// NoTrimToolCallIDs 是不允许被裁剪或回收的 ToolCallID 集合。调用方（例如负责 output-spill
	// 恢复的层）通过它保护携带恢复标记的工具结果，使预算裁剪与元数据回收都不会抹掉标记。
	// nil 表示不保护任何结果，行为与旧版逐字节一致。该集合由调用方注入，agent 包不依赖 session/tools。
	NoTrimToolCallIDs map[string]bool
	// CacheProtectSuffixTokens 覆盖提示缓存保护阈值。零表示使用保守的默认值。
	CacheProtectSuffixTokens int
	// TargetRatio 是预算循环所瞄准的 MaxTokens 比例。(0, 1) 内的值会使 Trim 向下超额到该比例，
	// 留出余量，使下一轮次不会立即再次触发裁剪并使提示缓存失效。
	// 零值（默认）以及 >= 1 的值保留精确裁剪到 MaxTokens 的原有行为。
	TargetRatio float64
}

// TrimResult 报告裁剪做了什么。
type TrimResult struct {
	Messages []llm.Message
	// Pruned 是被替换为占位条目的工具结果数量。
	Pruned int
	// Freed 是节省的估算 token 数。
	Freed int
}

// fullBudgetRatio 是保留原有行为的 TargetRatio 值：精确裁剪到 MaxTokens，不留增长余量。
const fullBudgetRatio = 1.0

// budgetTarget 返回预算循环所瞄准的 token 水平。
//
// (0, 1) 内的 TargetRatio 请求迟滞：裁剪到 MaxTokens 以下，使下一轮次在再次触发裁剪前
// 有增长空间，从而保持提示缓存温热。零值（默认）以及 >= 1 的比例保留精确裁剪到 MaxTokens
// 的原有行为，因此现有调用方不受影响。
func budgetTarget(maxTokens int, ratio float64) int {
	if ratio <= 0 || ratio >= fullBudgetRatio {
		return maxTokens
	}
	target := int(float64(maxTokens) * ratio)
	if target < 1 {
		return 1
	}
	return target
}

// Trim 通过把旧的工具输出替换为一行占位条目来缩小对话。它从不删除消息，
// 因此工具调用与其结果保持配对。
//
// 始终原样保留：每条系统消息、第一条用户消息、最后 KeepTurns 个轮次，
// 以及最近的 KeepToolTokens 量工具输出。较旧的工具主体是最廉价的可回收对象——
// 调用及其状态仍然可见，因此模型仍知道做过什么。
func Trim(messages []llm.Message, opts TrimOptions) TrimResult {
	out := append([]llm.Message(nil), messages...)
	res := TrimResult{Messages: out}
	// 基于元数据的回收很廉价（不调用模型），且与 token 预算无关，
	// 因此即使调用方禁用了大小裁剪，它也先运行。
	if len(opts.ToolMeta) > 0 {
		reclaimByMeta(out, opts, &res)
	}
	if opts.MaxTokens <= 0 {
		return res
	}
	total := EstimateMessages(out)
	if total <= opts.MaxTokens {
		return res
	}
	// 迟滞：触发点保持在 MaxTokens，但循环瞄准其下方，
	// 使一次成功的裁剪留下余量，而不是在下一轮次就再次触发。
	target := budgetTarget(opts.MaxTokens, opts.TargetRatio)

	keepTurns := opts.KeepTurns
	if keepTurns <= 0 {
		keepTurns = 6
	}
	keepToolTokens := opts.KeepToolTokens
	if keepToolTokens <= 0 {
		keepToolTokens = 2000
	}

	effectiveProtect := effectiveCacheProtect(opts)
	protected := protectedIndexes(out, keepTurns)
	recentTool := keepToolTokens
	// suffix 是 out[i+1:] 的 EstimateMessage 总和，随着 i 向前移动而维护，
	// 使下方的保护判断保持 O(1)，而不必对每个工具结果重新扫描尾部。
	// 把 out[i] 替换为占位条目会改变构成 suffix 的主体，因此后续位置看到的是
	// 替换后的开销（cost - freed）。
	suffix := 0
	for i := len(out) - 1; i >= 0; i-- {
		msg := out[i]
		if msg.Role != "tool" || protected[i] || opts.NoTrimToolCallIDs[msg.ToolCallID] {
			suffix += EstimateMessage(msg)
			continue
		}
		// 预算循环向后遍历，因此随着 i 前移，suffix 只会增长。一旦替换占位条目会重写
		// 一段其后仍缓存着超过 protect 个 token 的字节范围，那么更早的每个位置同样不安全：
		// 此时应停止，而不是使温热的前缀失效并触发一次完整的重新预填充。
		// 这与 reclaimByMeta 中的保护逻辑一致。
		if suffix > effectiveProtect {
			break
		}
		cost := EstimateMessage(msg)
		if recentTool > 0 {
			recentTool -= cost
			suffix += cost
			continue
		}
		// 错误结果必须保留原文：压缩不得抹掉失败记录，模型可能仍需要这些细节。
		// reclaimByMeta 同样跳过错误结果，这里保持一致，避免失败被 stub 成占位条目。
		if isErrorResult(msg.Content) {
			suffix += cost
			continue
		}
		if msg.Content == "" || strings.HasPrefix(msg.Content, trimmedPrefix) {
			suffix += cost
			continue
		}
		stub := toolStub(msg.Content)
		stubbed := msg
		stubbed.Content = stub
		freed := cost - EstimateMessage(stubbed)
		if freed <= 0 {
			suffix += cost
			continue
		}
		out[i] = stubbed
		res.Pruned++
		res.Freed += freed
		total -= freed
		suffix += cost - freed
		if total <= target {
			break
		}
	}
	return res
}

// protectedIndexes 标记裁剪不得触碰的消息。
func protectedIndexes(messages []llm.Message, keepTurns int) []bool {
	protected := make([]bool, len(messages))
	for i, msg := range messages {
		if msg.Role == "system" {
			protected[i] = true
		}
	}
	for i, msg := range messages {
		if msg.Role == "user" {
			protected[i] = true
			break
		}
	}
	seen := 0
	start := len(messages)
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "assistant" {
			continue
		}
		seen++
		start = i
		if seen >= keepTurns {
			break
		}
	}
	for i := start; i < len(messages); i++ {
		protected[i] = true
	}
	return protected
}

// pruneToolResultMiddle 保留超预算工具结果的开头和结尾，并用 toolResultPruneMarker
// 替换被移除的中间部分。
//
// 切片按 rune 而非字节进行，因此跨越预算边界的多字节 UTF-8 字符（CJK、emoji）
// 会保持完整，而不会被切断。在此基础上，head/tail 的字节切点还会吸附到字素簇
// 边界：head 只回退、tail 只前进，仅当切点落在 ZWJ/combining/旗帜/jamo 等
// 多 rune 字素簇中间时才调整，单 rune 字素的默认路径逐字节不变。
// 大小在 toolResultPruneThreshold 以下的结果原样返回，这使该函数可以无条件安全地调用。
func pruneToolResultMiddle(content string) string {
	// 字节早退：每个 rune 至少占 1 字节，字节数不超阈值时 rune 数必然也不超，
	// 避免为绝大多数短结果做全文 []rune 转换。
	if len(content) <= toolResultPruneThreshold {
		return content
	}

	// 合法 UTF-8（绝大多数情况）直接在原串上按 rune 计数与切片，省掉
	// []rune→string 的往返分配。畸形字节才回退到原来的归一化路径：
	// 归一后的合法串使字素边界函数里的 RuneStart 检查不会落在畸形序列中间。
	encoded := content
	runeCount := utf8.RuneCountInString(content)
	if !utf8.ValidString(content) {
		runes := []rune(content)
		runeCount = len(runes)
		encoded = string(runes)
	}
	if runeCount <= toolResultPruneThreshold {
		return content
	}
	// 让替换结果严格小于原文。固定的阈值/预算关系已经保证了这一点，
	// 但仍加上保护，以免未来的预算改动把"裁剪"变成增长。
	if toolResultPruneHead+toolResultPruneTail >= runeCount {
		return content
	}

	headByte := runeByteOffset(encoded, toolResultPruneHead)
	tailByte := runeByteOffset(encoded, runeCount-toolResultPruneTail)
	// 字素边界吸附：head 只回退、tail 只前进。切点已在簇边界时原样返回。
	headByte = textutil.ClusterBoundaryBefore(encoded, headByte)
	tailByte = textutil.ClusterBoundaryAfter(encoded, tailByte)
	headRune := utf8.RuneCountInString(encoded[:headByte])
	tailRune := utf8.RuneCountInString(encoded[:tailByte])
	// 防御：head 回退、tail 前进只会扩大间距，这里仍保留保护以免未来预算
	// 改动让 head 越过 tail 而产生重叠切片。字节与 rune 切点同步回退，
	// 以便直接用 encoded 切片，不再把 rune 切片重新编码成字符串。
	if headRune > tailRune {
		headRune, headByte = tailRune, tailByte
	}

	var b strings.Builder
	b.Grow(len(content) + len(toolResultPruneMarker))
	b.WriteString(encoded[:headByte])
	b.WriteString(toolResultPruneMarker)
	b.WriteString(encoded[tailByte:])
	return b.String()
}

// runeByteOffset 返回 encoded 中前 n 个 rune 所占的字节数。encoded 是由 []rune
// 重编码而来的合法 UTF-8，因此每个 rune 的字节数可由解码精确累加得到。
func runeByteOffset(encoded string, n int) int {
	offset := 0
	for k := 0; k < n && offset < len(encoded); k++ {
		_, size := utf8.DecodeRuneInString(encoded[offset:])
		offset += size
	}
	return offset
}

// toolStub 为被裁剪的工具结果构建替换文本。trimmedPrefix 头部保持在最前面，
// 使每个占位检测点都能识别它。对于超预算的主体，开头和结尾会围绕一个标记保留，
// 让模型保有一些细节；较小的主体则回退到紧凑的一行占位条目，使预算循环仍能回收空间。
func toolStub(content string) string {
	pruned := pruneToolResultMiddle(content)
	removed := utf8.RuneCountInString(content) - utf8.RuneCountInString(pruned)
	header := fmt.Sprintf("%s %d chars of this tool result were removed to save context; run the tool again if the details matter]", trimmedPrefix, removed)
	if pruned == content {
		return header
	}
	return header + "\n" + pruned
}

// reclaimByMeta 标记那些被后续轮次变得多余的工具结果。它从不删除消息：
// 紧凑的占位条目总会取代旧主体，因此助手调用与其结果保持配对。
//
// 这里进行两种回收：
//   - 某个读取的键在之后一次成功的读取中再次出现，则它被取代；
//   - 一旦存在任何更晚的助手轮次（即模型已有机会消费它），无用的（空）结果就会被省略。
//
// 后缀很大的结果会被跳过：为一点节省而使一大段提示缓存前缀失效并不划算。
func reclaimByMeta(messages []llm.Message, opts TrimOptions, res *TrimResult) {
	protect := effectiveCacheProtect(opts)

	// 一次反向遍历预先计算出旧循环为每条工具消息重新计算的两种尾部查询。
	// suffixToken[i] 等于 i 之后所有内容的估算值；nextAssistantAfter[i] 记录 i 之后是否有助手轮次。
	// 下方的改写循环向前遍历，且只触碰自身索引处的消息，因此它查询的每个索引（> i）
	// 在那时仍是原封不动的——预计算的答案与旧的按需扫描完全一致，但每次是 O(1) 而非 O(n)。
	suffixToken := make([]int, len(messages))
	nextAssistantAfter := make([]bool, len(messages))
	runningTokens := 0
	sawAssistant := false
	for i := len(messages) - 1; i >= 0; i-- {
		suffixToken[i] = runningTokens
		nextAssistantAfter[i] = sawAssistant
		runningTokens += EstimateMessage(messages[i])
		if messages[i].Role == "assistant" {
			sawAssistant = true
		}
	}

	// newestSuccessful[key] 是携带该键且不是错误的最后一条结果。只有之后一次成功的读取
	// 才能取代较旧的读取：错误绝不能抹掉模型可能仍需要的内容。
	newestSuccessful := make(map[string]int)
	for i, msg := range messages {
		if msg.Role != "tool" || isErrorResult(msg.Content) {
			continue
		}
		if key := opts.ToolMeta[msg.ToolCallID].SupersedeKey; key != "" {
			newestSuccessful[key] = i
		}
	}

	for i, msg := range messages {
		if msg.Role != "tool" || isErrorResult(msg.Content) || opts.NoTrimToolCallIDs[msg.ToolCallID] {
			continue
		}
		if msg.Content == supersededStub || msg.Content == uselessStub || strings.HasPrefix(msg.Content, trimmedPrefix) {
			continue // 已在更早的轮次被回收
		}
		meta := opts.ToolMeta[msg.ToolCallID]
		if meta == (ToolMeta{}) {
			continue
		}
		stub := ""
		if meta.SupersedeKey != "" {
			if later, ok := newestSuccessful[meta.SupersedeKey]; ok && later > i {
				stub = supersededStub
			}
		}
		if stub == "" && meta.Useless && nextAssistantAfter[i] {
			stub = uselessStub
		}
		if stub == "" {
			continue
		}
		if suffixToken[i] > protect {
			continue
		}
		replacement := msg
		replacement.Content = stub
		before := EstimateMessage(msg)
		after := EstimateMessage(replacement)
		// 基于元数据的回收是语义性的：即使占位条目多出几个 token，空搜索结果仍会被替换，
		// 因为目的在于阻止模型反复读取一个没有携带信息的结果。
		messages[i] = replacement
		res.Pruned++
		if saved := before - after; saved > 0 {
			res.Freed += saved
		}
	}
}

// isErrorResult 识别循环为失败的工具写入的错误结果。错误文本绝不会成为回收的候选。
func isErrorResult(content string) bool {
	return strings.HasPrefix(content, "error:")
}

// effectiveCacheProtect 返回提示缓存保护阈值。语义回收（reclaimByMeta）和预算裁剪（Trim）
// 两条路径都使用它，使带有大段缓存后缀的结果不会被任何一条路径改写。
func effectiveCacheProtect(opts TrimOptions) int {
	if opts.CacheProtectSuffixTokens > 0 {
		return opts.CacheProtectSuffixTokens
	}
	return cacheProtectSuffixTokens
}
