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

// canonicalCompactMessage is the byte-stable projection of an llm.Message used
// only for prefix hashing. It deliberately carries just the fields that change
// the *meaning* of the conversation.
//
// Excluded on purpose because they vary per round-trip and are documented as
// transcript/wire metadata on llm.Message:
//   - Reasoning, Usage, FinishReason, CacheControl
//   - ToolCall.ID and Message.ToolCallID (provider-issued ids, not content)
//
// Keeping the projection in a fixed-layout struct (rather than a map) gives a
// deterministic field order under encoding/json, so the resulting hash is
// stable across processes and Go versions.
type canonicalCompactMessage struct {
	Role    string                 `json:"role"`
	Content string                 `json:"content"`
	Tools   []canonicalCompactTool `json:"tools"`
}

// canonicalCompactTool keeps the tool action itself but drops its provider id.
type canonicalCompactTool struct {
	Type      string `json:"type"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

const defaultSummaryCacheCapacity = 64

// SourcePrefixHash returns a stable, fixed-length hex SHA-256 key for a run
// prefix. Two prefixes that differ only in volatile metadata (or in user text
// that differs solely by surrounding whitespace) produce the same key, which
// lets a compactor reuse a previously finished summary instead of re-summarising
// the same content every turn.
//
// It is a pure function: it never mutates the input slice or its messages.
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
		// The canonical types contain only strings and slices, so this cannot
		// fail in practice; return the zero value rather than panicking.
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// summaryCacheEntry is one finished summary held by SummaryCache.
type summaryCacheEntry struct {
	prefixHash string
	summary    string
}

// SummaryCache stores *finished* summaries keyed by SourcePrefixHash. It is
// safe for concurrent use. At capacity it evicts the oldest inserted entry
// (FIFO by first insertion); updating an existing key keeps its original
// position. Get does not reorder, so a hot entry can still age out.
//
// The zero value is ready to use and defaults to defaultSummaryCacheCapacity.
type SummaryCache struct {
	mu       sync.Mutex
	capacity int
	order    *list.List               // front = oldest insertion, back = newest
	index    map[string]*list.Element // prefixHash -> element
}

// NewSummaryCache returns a cache with an explicit capacity. A non-positive
// capacity falls back to defaultSummaryCacheCapacity.
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

// Get returns the finished summary stored for prefixHash, if any.
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

// Put stores a finished summary for prefixHash. An empty summary is rejected:
// it is the zero/"not finished yet" value, and a cache must never hold a
// half-built summary that a later caller could mistake for a real result.
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
