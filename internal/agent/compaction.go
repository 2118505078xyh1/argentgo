package agent

import (
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// Failure 表示一次运行中发生的工具或提供方失败。
type Failure struct {
	Turn   int
	Tool   string
	Reason string
	Error  bool // 提供方级别的错误事件为 true
}

// CompactionEvidence 是压缩器必须保留的运行时信息。
// 会话压缩器应把 Failures 和 NextSteps 带进摘要：
// 丢弃它们恰好会丢失恢复运行最需要的上下文。
type CompactionEvidence struct {
	Failures  []Failure
	NextSteps []string
}

const (
	evidenceMaxSteps  = 20
	evidenceStepRunes = 240
)

var nextStepPrefixes = []string{
	"next:", "next step", "next steps", "todo:", "follow up:", "follow-up:",
	"remaining:", "下一步", "后续",
}

// CollectCompactionEvidence 从一次运行的事件和模型可见消息中，提取失败记录与明确的后续步骤行。
func CollectCompactionEvidence(events []Event, messages []llm.Message) CompactionEvidence {
	out := CompactionEvidence{}
	for _, event := range events {
		switch {
		case event.Kind == EventToolEnd && event.IsError:
			out.Failures = append(out.Failures, Failure{
				Turn:   event.Turn,
				Tool:   event.ToolName,
				Reason: trimEvidence(event.Output),
			})
		case event.Kind == EventError:
			out.Failures = append(out.Failures, Failure{
				Turn:   event.Turn,
				Reason: trimEvidence(event.Err),
				Error:  true,
			})
		}
	}
	seen := make(map[string]bool)
	for _, message := range messages {
		if message.Role != "assistant" || message.Content == "" {
			continue
		}
		for _, line := range strings.Split(message.Content, "\n") {
			line = strings.TrimSpace(line)
			if !isNextStep(line) {
				continue
			}
			line = trimEvidence(line)
			if line == "" || seen[line] {
				continue
			}
			seen[line] = true
			out.NextSteps = append(out.NextSteps, line)
			if len(out.NextSteps) >= evidenceMaxSteps {
				return out
			}
		}
	}
	return out
}

// isNextStep 识别模型用来记录未完成工作的简短显式标记。它刻意保持严格：
// 普通正文并不算计划。
func isNextStep(line string) bool {
	lowered := strings.ToLower(line)
	for _, prefix := range nextStepPrefixes {
		if strings.HasPrefix(lowered, prefix) {
			return true
		}
	}
	return false
}

// trimEvidence 为压缩摘要保留一行可读、rune 安全的文本。
func trimEvidence(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(text) <= evidenceStepRunes {
		return text
	}
	runes := []rune(text)
	return string(runes[:evidenceStepRunes]) + "..."
}

// SortFailuresByTurn 返回按轮次、再按工具排序的失败记录，
// 这样从不同事件来源写出的摘要都是字节稳定的。
func SortFailuresByTurn(failures []Failure) []Failure {
	out := append([]Failure(nil), failures...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Turn != out[j].Turn {
			return out[i].Turn < out[j].Turn
		}
		if out[i].Tool != out[j].Tool {
			return out[i].Tool < out[j].Tool
		}
		return out[i].Reason < out[j].Reason
	})
	return out
}

// cachingCompactor 按 SourcePrefixHash 缓存 Compactor 的输出，
// 因此相同的运行前缀永远不会被重复摘要。它是 SummaryCache 在 agent 侧的接入点：
// 调用点仍然是注入的 Compactor，但命中缓存会完全跳过它，直接重放已完成的视图。
//
// 缓存的值是压缩后视图的 JSON（其中内嵌了摘要文本）。存储整个视图而非仅摘要字符串，
// 能让缓存命中与原始摘要逐字节一致，同时无需在这个无依赖的包里重复 app/session 的裁剪逻辑。
//
// 键仅仅是源前缀哈希。压缩窗口由单个 Runner 固定的触发阈值推导得出，
// 因此在该 Runner 持有的缓存生命周期内它是稳定的。
type cachingCompactor struct {
	inner Compactor
	cache *SummaryCache

	// mu 串行化整个缓存旁路区。压缩通常运行在单一的循环 goroutine 上；
	// 该锁使并发的调用突增（以及有副作用的内部钩子）保持 single-flight，
	// 因此同一个前缀绝不会被多个 goroutine 同时摘要。
	mu sync.Mutex
}

// newCachingCompactor 用摘要缓存包装 inner。inner 为 nil 时仍返回 nil
// （保留"无压缩器"的行为）；cache 为 nil 时会新建一个默认容量的缓存。
func newCachingCompactor(inner Compactor, cache *SummaryCache) Compactor {
	if inner == nil {
		return nil
	}
	if cache == nil {
		cache = NewSummaryCache(0)
	}
	return &cachingCompactor{inner: inner, cache: cache}
}

// MaybeCompact 实现 Compactor 接口。
func (c *cachingCompactor) MaybeCompact(messages []llm.Message, window int) ([]llm.Message, bool) {
	hash := SourcePrefixHash(messages)
	c.mu.Lock()
	defer c.mu.Unlock()

	if encoded, ok := c.cache.Get(hash); ok {
		if out, ok := decodeCompactedView(encoded); ok {
			return out, true
		}
		// 损坏/过期的条目不能改变压缩语义：继续走真正的摘要器并覆盖它。
	}

	out, changed := c.inner.MaybeCompact(messages, window)
	if changed && len(out) > 0 {
		c.cache.Put(hash, encodeCompactedView(out))
	}
	return out, changed
}

func encodeCompactedView(view []llm.Message) string {
	data, err := json.Marshal(view)
	if err != nil {
		return ""
	}
	return string(data)
}

func decodeCompactedView(encoded string) ([]llm.Message, bool) {
	var view []llm.Message
	if err := json.Unmarshal([]byte(encoded), &view); err != nil || len(view) == 0 {
		return nil, false
	}
	return view, true
}
