package agent

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// canonicalCompactMessage 是 llm.Message 的字节稳定投影，仅用于前缀哈希。
// 它刻意只携带会改变对话*含义*的字段。
//
// 以下字段被有意排除，因为它们每次往返都会变化，且在 llm.Message 中被标记为
// 记录/线上元数据：
//   - Reasoning、Usage、FinishReason、CacheControl
//   - ToolCall.ID 和 Message.ToolCallID（由提供方签发的 id，不是内容）
//
// 把投影保存在固定布局的结构体（而非 map）中，可在 encoding/json 下获得确定的字段顺序，
// 因此生成的哈希在不同进程和 Go 版本之间都是稳定的。
type canonicalCompactMessage struct {
	Role    string                 `json:"role"`
	Content string                 `json:"content"`
	Tools   []canonicalCompactTool `json:"tools"`
}

// canonicalCompactTool 保留工具动作本身，但去掉其提供方 id。
type canonicalCompactTool struct {
	Type      string `json:"type"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

const defaultSummaryCacheCapacity = 64

// SourcePrefixHash 为运行前缀返回一个稳定、定长的十六进制 SHA-256 键。
// 仅在易变元数据上不同（或用户文本仅两端空白不同）的两个前缀会产生相同的键，
// 使压缩器能复用之前已完成的摘要，而不必每个轮次都对相同内容重新摘要。
//
// 它是纯函数：从不修改输入切片或其消息。
func SourcePrefixHash(messages []llm.Message) string {
	canonical := make([]canonicalCompactMessage, 0, len(messages))
	for _, message := range messages {
		content := message.Content
		if message.Role == "user" {
			content = strings.TrimSpace(content)
		}
		tools := make([]canonicalCompactTool, 0, len(message.ToolCalls))
		for _, call := range message.ToolCalls {
			tools = append(tools, canonicalCompactTool{
				Type:      call.Type,
				Name:      call.Function.Name,
				Arguments: call.Function.Arguments,
			})
		}
		canonical = append(canonical, canonicalCompactMessage{
			Role:    message.Role,
			Content: content,
			Tools:   tools,
		})
	}

	data, err := json.Marshal(canonical)
	if err != nil {
		// 规范化类型只包含字符串和切片，因此实际上不会失败；
		// 这里返回零值而不是 panic。
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// summaryCacheEntry 是 SummaryCache 持有的一条已完成摘要。
type summaryCacheEntry struct {
	prefixHash string
	summary    string
}

// SummaryCache 按 SourcePrefixHash 存储*已完成*的摘要。它可安全并发使用。
// 达到容量时，会淘汰最早插入的条目（按首次插入的 FIFO 顺序）；更新已有键会保留其原始位置。
// Get 不会调整顺序，因此热点条目仍可能被淘汰。
//
// 零值即可使用，默认容量为 defaultSummaryCacheCapacity。
type SummaryCache struct {
	mu       sync.Mutex
	capacity int
	order    *list.List               // 前端 = 最早插入，后端 = 最新
	index    map[string]*list.Element // prefixHash -> 元素
}

// NewSummaryCache 返回具有指定容量的缓存。非正容量会回退到 defaultSummaryCacheCapacity。
func NewSummaryCache(capacity int) *SummaryCache {
	c := &SummaryCache{capacity: capacity}
	c.initLocked()
	return c
}

func (c *SummaryCache) initLocked() {
	if c.order == nil {
		c.order = list.New()
	}
	if c.index == nil {
		c.index = make(map[string]*list.Element)
	}
	if c.capacity <= 0 {
		c.capacity = defaultSummaryCacheCapacity
	}
}

// Get 返回为 prefixHash 存储的已完成摘要（如果有）。
func (c *SummaryCache) Get(prefixHash string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.initLocked()
	element, ok := c.index[prefixHash]
	if !ok {
		return "", false
	}
	return element.Value.(*summaryCacheEntry).summary, true
}

// Put 为 prefixHash 存储一条已完成的摘要。空摘要会被拒绝：
// 它是零值/"尚未完成"的标记，缓存绝不能持有一条半成品摘要，
// 以免之后的调用方把它误当成真实结果。
func (c *SummaryCache) Put(prefixHash, summary string) {
	if summary == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.initLocked()

	if element, ok := c.index[prefixHash]; ok {
		element.Value.(*summaryCacheEntry).summary = summary
		return
	}
	for c.order.Len() >= c.capacity {
		oldest := c.order.Front()
		if oldest == nil {
			break
		}
		delete(c.index, oldest.Value.(*summaryCacheEntry).prefixHash)
		c.order.Remove(oldest)
	}
	element := c.order.PushBack(&summaryCacheEntry{prefixHash: prefixHash, summary: summary})
	c.index[prefixHash] = element
}
