package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"example.com/argentgo/internal/agentpolicy"
	"example.com/argentgo/internal/llm"
	"example.com/argentgo/internal/prof"
	"example.com/argentgo/internal/textutil"
)

// MaxCompactRetries 是循环针对提供方报告的上下文溢出，可以进行的有上限的
// "压缩后重试"次数。一次就足够：如果同一个超长请求在压缩后仍然溢出，
// 再次重放只会消耗轮次预算，而不会带来任何进展。
// 纯决策实现已下沉到 internal/agentpolicy，这里保留常量别名使调用点不变。
const MaxCompactRetries = agentpolicy.MaxCompactRetries

// CompactRetryDecision 是模型请求失败后循环的选择。"retry" 会把记录压缩一次并重发同一轮次；
// "stop" 则保持原有的失败路径不变。
type CompactRetryDecision = agentpolicy.CompactRetryDecision

const (
	CompactRetry = agentpolicy.CompactRetry
	CompactStop  = agentpolicy.CompactStop
)

// DecideCompactRetry 委托给 agentpolicy 的纯决策实现。它从不压缩、发出事件或修改状态；
// 这些副作用由调用方负责。
func DecideCompactRetry(kind llm.FailureKind, retriesSoFar int, maxRetries int) CompactRetryDecision {
	return agentpolicy.DecideCompactRetry(kind, retriesSoFar, maxRetries)
}

// compactRetryWindow 给出压缩重试时传给 Compactor 的目标窗口。
// 配置了阈值时复用轮次中压缩的 0.8 迟滞带，使一次压缩留出余量；
// 未配置阈值时退回到上一次请求的提示估算（首次请求即溢出时为零，回落到最小窗口）。
func (s *runState) compactRetryWindow() int {
	var window int
	if s.compactThreshold > 0 {
		window = int(float64(s.compactThreshold)*compactionRecoveryBand) - summaryReserveTokens
	} else {
		window = int(float64(s.promptTokens) * compactionRecoveryBand)
	}
	if window < minCompactWindow {
		window = minCompactWindow
	}
	// 上下文计量校准（opt-in）：重试时按 SHRINK_STEPS 逐级缩小窗口，
	// 让重发的请求更容易落在提供方限制之内。关闭时窗口逐字节不变。
	if s.contextCalibration && s.compactRetries < ShrinkStepCount() {
		window = ShrinkWindow(window, s.compactRetries)
		if window < minCompactWindow {
			window = minCompactWindow
		}
	}
	return window
}

// compactAndReplay 处理一次由模型请求暴露的上下文溢出：压缩记录后在同一个轮次重发一次请求。
//
// 返回的 retried 表示是否真的重放了请求。调用方仅在 retried 为真时用 (reply, err) 取代
// 首次请求的结果；为假时必须保持原有的失败路径逐字节不变。
//
// 保守性：只有 opt-in 开关已开启、配置了 Compactor、失败类别为上下文溢出且
// DecideCompactRetry 判定仍有预算时才行动；压缩没有真正缩短记录时也不会白放一次请求。
// 计数器在重放之前自增，因此无论重放成功还是再次失败，每次运行最多只重放一次，
// 同一份超长请求不会反复空转。
func (s *runState) compactAndReplay(ctx context.Context, turn int, failure llm.FailureInfo) (llm.Message, error, bool) {
	defer prof.Region(prof.RegionCompact).End()
	if !s.opts.EnableCompactRetry || s.compactor == nil {
		return llm.Message{}, nil, false
	}
	if DecideCompactRetry(failure.Kind, s.compactRetries, MaxCompactRetries) != CompactRetry {
		return llm.Message{}, nil, false
	}

	before := EstimateMessagesBatch(s.messages)
	window := s.compactRetryWindow()
	compacted, changed := s.compactor.MaybeCompact(s.messages, window)
	if !changed || len(compacted) == 0 {
		return llm.Message{}, nil, false
	}
	s.messages = compacted
	// 摘要后的上下文是一个新窗口：与轮次中压缩一样清除提醒投递状态并刷新提示估算，
	// 使重放之后的预算检查看到真实的历史大小，而不是旧的超长记录。
	s.resetBudgetReminder()
	s.promptTokens = EstimateMessagesBatch(s.messages)
	// 默认开启（ARGENT_COMPACT_DRAIN=0 关闭）：压缩后仍超预算时按缓存前缀逐条淘汰最旧消息，保留 system 与末尾提示。
	if compactDrainEnabled() && s.promptTokens > window {
		if drained, n := drainOldestForBudget(s.messages, window, maxCompactDrain); n > 0 {
			s.messages = drained
			s.promptTokens = EstimateMessagesBatch(s.messages)
			s.emit(Event{Kind: EventNotice, Text: fmt.Sprintf(
				"压缩后仍超限，已淘汰 %d 条最旧消息，剩余 ~%d tokens", n, s.promptTokens,
			)})
		}
	}
	s.compactRetries++
	s.emit(Event{Kind: EventNotice, Text: fmt.Sprintf(
		"上下文溢出：已将记录从 ~%d 压缩到 ~%d tokens，重放第 %d 轮", before, s.promptTokens, turn,
	)})

	// 重放同样是一次模型请求：和首次请求一样先经过持久性请求屏障。
	// 屏障错误会作为本次重放的结果返回，由调用方按既有失败路径分类并发出事件。
	if err := s.runBeforeRequest(); err != nil {
		return llm.Message{}, err, true
	}
	reply, err := s.chatWithFallback(ctx, turn)
	return reply, err, true
}

// 压缩请求自身超限（TODO #3）的默认参数。全部取条目给出的最优值，用户零选择：
// 分块预算约为可用窗口的 0.6，单块摘要输出上限 2048，递归归约深度 ≤3、并发 ≤3。
const (
	compactionPayloadChunkRunes    = 12000
	compactionSummaryMaxOutput     = 2048
	compactionReduceMaxDepth       = 3
	compactionReduceMaxConcurrency = 3
	compactionStripToolRunes       = 512
	compactionMediaOmitted         = "[media omitted]"
	compactionChunkPrompt          = "Summarize the following conversation chunk for continuation. " +
		"Preserve goals, changed files, failures and next steps. Keep the summary concise."
)

// IsCompactionPayloadOverflow 判断错误是否表示"压缩请求自身体积超限"。
// 匹配 request entity too large / function_payload_too_large / 413 等稳定模式，
// 大小写不敏感；nil 返回 false。它是纯文本判定，不读取任何外部状态。
func IsCompactionPayloadOverflow(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, pattern := range []string{
		"request entity too large",
		"function_payload_too_large",
		"payload too large",
		"request body too large",
		"413 payload too large",
	} {
		if strings.Contains(message, pattern) {
			return true
		}
	}
	return false
}

// StripCompactionPayload 剥离已完成工具的冗长输出与媒体附件，返回新消息切片和被剥离的数量。
// 它在压缩请求自身超限时作为第一道恢复：先丢掉可再生的体积，再谈分块摘要。
// 入参不被修改；媒体 part 以有界文本占位替代，保留可检索事实。
func StripCompactionPayload(messages []llm.Message) ([]llm.Message, int) {
	out := make([]llm.Message, len(messages))
	copy(out, messages)
	stripped := 0
	for i := range out {
		message := &out[i]
		if len(message.ContentParts) > 0 {
			message.ContentParts = nil
			if strings.TrimSpace(message.Content) == "" {
				message.Content = compactionMediaOmitted
			}
			stripped++
		}
		if message.Role != "tool" || utf8.RuneCountInString(message.Content) <= compactionStripToolRunes {
			continue
		}
		message.Content = textutil.ClipRunes(message.Content, compactionStripToolRunes)
		stripped++
	}
	return out, stripped
}

// messageRuneSize 估算一条消息的 rune 体积，用于决定分块边界。
// ContentParts 是原始 JSON，直接计入长度；不做任何解码，因此是保守估计。
func messageRuneSize(message llm.Message) int {
	return utf8.RuneCountInString(message.Content) + len(message.ContentParts)
}

// SplitMessagesForCompaction 按 rune 预算把消息切成连续块（0.6×窗口由调用方给出预算）。
// 单条消息超预算时独占一块，绝不切坏消息边界；返回的新切片不共享底层数组。
func SplitMessagesForCompaction(messages []llm.Message, runeBudget int) [][]llm.Message {
	if runeBudget <= 0 {
		runeBudget = compactionPayloadChunkRunes
	}
	var chunks [][]llm.Message
	current := make([]llm.Message, 0, 8)
	used := 0
	for _, message := range messages {
		size := messageRuneSize(message)
		if len(current) > 0 && used+size > runeBudget {
			chunks = append(chunks, current)
			current = make([]llm.Message, 0, 8)
			used = 0
		}
		current = append(current, message)
		used += size
	}
	if len(current) > 0 {
		chunks = append(chunks, current)
	}
	return chunks
}

// ChunkedCompactionReduce 逐块摘要并递归两两归约，得到单个有界摘要。
// summarize 由调用方注入（通常是一次次级模型调用），使本函数是纯策略、可直接测试。
// depth ≤ maxDepth，每轮并发 ≤ maxConcurrency；到深度上限时直接拼接剩余摘要，
// 避免无限递归。输出按 maxOutput 个 rune 有界裁剪。
func ChunkedCompactionReduce(
	chunks [][]llm.Message,
	summarize func([]llm.Message) (string, error),
	maxDepth, maxConcurrency, maxOutput int,
) (string, error) {
	if len(chunks) == 0 {
		return "", nil
	}
	if summarize == nil {
		return "", fmt.Errorf("agent: chunked compaction needs a summarizer")
	}
	if maxDepth <= 0 {
		maxDepth = compactionReduceMaxDepth
	}
	if maxConcurrency <= 0 {
		maxConcurrency = compactionReduceMaxConcurrency
	}
	if maxOutput <= 0 {
		maxOutput = compactionSummaryMaxOutput
	}
	clamp := func(text string) string {
		return strings.TrimSpace(textutil.ClipRunes(text, maxOutput))
	}

	level := make([]string, len(chunks))
	if err := summarizeLevel(chunks, summarize, maxConcurrency, level); err != nil {
		return "", err
	}
	for depth := 1; depth < maxDepth && len(level) > 1; depth++ {
		pairs := make([][]llm.Message, 0, (len(level)+1)/2)
		for i := 0; i < len(level); i += 2 {
			pair := []llm.Message{{Role: "user", Content: level[i]}}
			if i+1 < len(level) {
				pair = append(pair, llm.Message{Role: "user", Content: level[i+1]})
			}
			pairs = append(pairs, pair)
		}
		merged := make([]string, len(pairs))
		if err := summarizeLevel(pairs, summarize, maxConcurrency, merged); err != nil {
			return "", err
		}
		level = merged
	}
	return clamp(strings.Join(level, "\n\n")), nil
}

// summarizeLevel 以有界并发摘要一批输入，结果写回与输入同下标的 out，
// 因此并发不会改变顺序；任何一块失败都取消本轮（用 context 由调用方决定是否重试）。
func summarizeLevel(
	inputs [][]llm.Message,
	summarize func([]llm.Message) (string, error),
	concurrency int,
	out []string,
) error {
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	for i, input := range inputs {
		wg.Add(1)
		go func(index int, messages []llm.Message) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			text, err := summarize(messages)
			mu.Lock()
			defer mu.Unlock()
			if err != nil && firstErr == nil {
				firstErr = err
				return
			}
			out[index] = text
		}(i, input)
	}
	wg.Wait()
	return firstErr
}

// recoverCompactionPayload 是压缩请求自身超限的完整恢复链：先剥离媒体/工具输出重试，
// 仍超限则分块摘要递归归约。它返回的 messages 已替换 s.messages，供调用方重放同一轮次。
// 该入口由装配层在捕获到 IsCompactionPayloadOverflow 的错误时调用（默认开启）。
func (s *runState) recoverCompactionPayload(ctx context.Context, rawErr error) ([]llm.Message, bool) {
	if !IsCompactionPayloadOverflow(rawErr) || s.opts.Chatter == nil {
		return nil, false
	}
	stripped, count := StripCompactionPayload(s.messages)
	if count > 0 {
		s.messages = stripped
		s.emit(Event{Kind: EventNotice, Text: fmt.Sprintf(
			"压缩请求体积超限：已剥离 %d 处媒体/工具输出后重试", count,
		)})
	}
	if EstimateMessagesBatch(s.messages) <= compactionPayloadChunkRunes {
		return s.messages, true
	}
	chunks := SplitMessagesForCompaction(s.messages, compactionPayloadChunkRunes)
	summarize := func(chunk []llm.Message) (string, error) {
		input := append([]llm.Message{{Role: "user", Content: compactionChunkPrompt}}, chunk...)
		reply, err := s.opts.Chatter.Chat(ctx, s.model, input, nil, nil)
		return reply.Content, err
	}
	summary, err := ChunkedCompactionReduce(chunks, summarize,
		compactionReduceMaxDepth, compactionReduceMaxConcurrency, compactionSummaryMaxOutput)
	if err != nil {
		// 次级摘要失败不阻断：至少已剥离体积，用剥离后的消息重放。
		return s.messages, true
	}
	s.messages = []llm.Message{{Role: "user", Content: summary}}
	s.emit(Event{Kind: EventNotice, Text: fmt.Sprintf(
		"压缩请求体积超限：已将 %d 块分块摘要递归归约为 ~%d tokens", len(chunks), EstimateMessagesBatch(s.messages),
	)})
	return s.messages, true
}
