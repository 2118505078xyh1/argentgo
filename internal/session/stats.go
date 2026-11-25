package session

import (
	"encoding/json"
	"math"
	"strings"

	"example.com/argentgo/internal/security"
)

// 用于统计和每次请求用量的记录 kind。会话格式中 kind 是开放的（见 session.go），
// 因此它们放在这里，而不是扩展核心 kind 表。
const (
	kindStats = "stats"
	kindUsage = "usage"
)

const (
	// modelLimit 限制用量记录中唯一的自由格式字符串，使单条 JSONL 行保持有界。
	// stampLimit 对调用方时间戳做同样的处理。
	modelLimit = 128
	stampLimit = 64

	costSourceReported  = "reported"
	costSourceEstimated = "estimated"
)

// SessionStats 是一次运行的持久化 token/成本汇总。将其存储为 `kind:"stats"` 记录
// 使会话状态能在恢复后存活：JSONL 转录是唯一的事实来源，而不是内存中的运行状态。
type SessionStats struct {
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	CachedTokens     int     `json:"cached_tokens,omitempty"`
	Calls            int     `json:"calls"`
	CostUSD          float64 `json:"cost_usd,omitempty"`
	CostSource       string  `json:"cost_source,omitempty"` // "reported" | "estimated"
	Updated          string  `json:"updated,omitempty"`
}

// UsageRecord 是一次计费请求的用量，用于成本溯源。它刻意区别于 SessionStats：
// 用量记录是原始的逐请求事实，而统计记录是调用方展示的汇总。
type UsageRecord struct {
	Model  string `json:"model,omitempty"`
	Input  int    `json:"input"`
	Output int    `json:"output"`
	Cached int    `json:"cached,omitempty"`
	TS     string `json:"ts,omitempty"`
}

// AppendStats 通过普通追加路径持久化一条统计记录，
// 因此它继承 id、分支以及每条记录的持久性保证。
func (s *Session) AppendStats(st SessionStats) error {
	_, err := s.AppendRecord(kindStats, normalizeStats(st))
	return err
}

// AppendUsage 持久化一条逐请求的用量记录。模型字符串和调用方时间戳在写入磁盘前
// 会被清理和限制；token 计数被限制为非负。
func (s *Session) AppendUsage(u UsageRecord) error {
	u.Model = scrubText(u.Model, modelLimit)
	u.TS = scrubText(u.TS, stampLimit)
	u.Input = max(0, u.Input)
	u.Output = max(0, u.Output)
	u.Cached = max(0, u.Cached)
	_, err := s.AppendRecord(kindUsage, u)
	return err
}

// LatestStats 返回活动分支上最新的统计记录，当该分支没有统计记录时，
// 回退到文件中任意位置最新的记录。布尔值报告是否找到任何统计记录。
func (s *Session) LatestStats() (SessionStats, bool) {
	records, err := s.Records()
	if err != nil {
		return SessionStats{}, false
	}
	active := make(map[string]bool)
	for _, record := range s.activePath(records) {
		active[record.ID] = true
	}
	var onBranch, newest SessionStats
	foundBranch, foundAny := false, false
	for _, record := range records {
		if record.Kind != kindStats {
			continue
		}
		st, ok := decodeStats(record.Data)
		if !ok {
			continue
		}
		newest, foundAny = st, true
		if active[record.ID] {
			onBranch, foundBranch = st, true
		}
	}
	if foundBranch {
		return onBranch, true
	}
	return newest, foundAny
}

// AggregateStats 按文件顺序将所有统计记录合并为一个汇总。每条记录是一次运行的增量，
// 因此 token 计数、调用次数和成本被相加；CostSource 和 Updated 来自携带它们的最新记录。
// 这就是跨恢复恢复总数的方式。
func (s *Session) AggregateStats() SessionStats {
	records, err := s.Records()
	if err != nil {
		return SessionStats{}
	}
	var total SessionStats
	for _, record := range records {
		if record.Kind != kindStats {
			continue
		}
		st, ok := decodeStats(record.Data)
		if !ok {
			continue
		}
		total.PromptTokens += st.PromptTokens
		total.CompletionTokens += st.CompletionTokens
		total.CachedTokens += st.CachedTokens
		total.Calls += st.Calls
		total.CostUSD += st.CostUSD
		if st.CostSource != "" {
			total.CostSource = st.CostSource
		}
		if st.Updated != "" {
			total.Updated = st.Updated
		}
	}
	return total
}

// UsageRecords 按追加顺序返回所有用量记录，用于成本审计。
func (s *Session) UsageRecords() []UsageRecord {
	records, err := s.Records()
	if err != nil {
		return nil
	}
	out := make([]UsageRecord, 0)
	for _, record := range records {
		if record.Kind != kindUsage {
			continue
		}
		var u UsageRecord
		if err := json.Unmarshal(record.Data, &u); err != nil {
			continue
		}
		out = append(out, u)
	}
	return out
}

func normalizeStats(st SessionStats) SessionStats {
	st.PromptTokens = max(0, st.PromptTokens)
	st.CompletionTokens = max(0, st.CompletionTokens)
	st.CachedTokens = max(0, st.CachedTokens)
	st.Calls = max(0, st.Calls)
	// NaN/Inf 无法编码为 JSON，会破坏整个追加。
	if st.CostUSD < 0 || math.IsNaN(st.CostUSD) || math.IsInf(st.CostUSD, 0) {
		st.CostUSD = 0
	}
	// 成本来源是封闭集合；丢弃其他内容也会丢弃调用方可能放在这个自由格式字段中的秘密。
	st.CostSource = normalizeCostSource(st.CostSource)
	st.Updated = scrubText(st.Updated, stampLimit)
	return st
}

func normalizeCostSource(src string) string {
	switch strings.ToLower(strings.TrimSpace(src)) {
	case costSourceReported:
		return costSourceReported
	case costSourceEstimated:
		return costSourceEstimated
	default:
		return ""
	}
}

func decodeStats(raw json.RawMessage) (SessionStats, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return SessionStats{}, false
	}
	var st SessionStats
	if err := json.Unmarshal(raw, &st); err != nil {
		return SessionStats{}, false
	}
	return st, true
}

func scrubText(text string, limit int) string {
	return truncateRunes(security.Redact(text), limit)
}
