package session

import (
	"encoding/json"
	"math"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// Record kinds for statistics and per-request usage. Kinds are intentionally
// open in the session format (see session.go), so these live here rather than
// extending the core kind table.
const (
	kindStats = "stats"
	kindUsage = "usage"
)

const (
	// modelLimit bounds the only free-form string on a usage record so a single
	// JSONL line stays bounded. stampLimit does the same for caller timestamps.
	modelLimit = 128
	stampLimit = 64

	costSourceReported  = "reported"
	costSourceEstimated = "estimated"
)

// SessionStats is a persisted token/cost roll-up for one run. Storing it as a
// `kind:"stats"` record makes session state survive resume: the JSONL
// transcript is the single source of truth, not in-memory run state.
type SessionStats struct {
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	CachedTokens     int     `json:"cached_tokens,omitempty"`
	Calls            int     `json:"calls"`
	CostUSD          float64 `json:"cost_usd,omitempty"`
	CostSource       string  `json:"cost_source,omitempty"` // "reported" | "estimated"
	Updated          string  `json:"updated,omitempty"`
}

// UsageRecord is one billable request's usage, kept for cost provenance. It is
// deliberately distinct from SessionStats: usage records are raw per-request
// facts, while stats records are the roll-up callers display.
type UsageRecord struct {
	Model  string `json:"model,omitempty"`
	Input  int    `json:"input"`
	Output int    `json:"output"`
	Cached int    `json:"cached,omitempty"`
	TS     string `json:"ts,omitempty"`
}

// AppendStats persists one stats record through the normal append path, so it
// inherits ids, branching and the durability guarantees of every other record.
func (s *Session) AppendStats(st SessionStats) error {
	_, err := s.AppendRecord(kindStats, normalizeStats(st))
	return err
}

// AppendUsage persists one per-request usage record. The model string and the
// caller timestamp are scrubbed and bounded before they reach disk; token
// counts are clamped non-negative.
func (s *Session) AppendUsage(u UsageRecord) error {
	u.Model = scrubText(u.Model, modelLimit)
	u.TS = scrubText(u.TS, stampLimit)
	u.Input = max(0, u.Input)
	u.Output = max(0, u.Output)
	u.Cached = max(0, u.Cached)
	_, err := s.AppendRecord(kindUsage, u)
	return err
}

// LatestStats returns the newest stats record on the active branch, falling
// back to the newest record anywhere in the file when the branch has none.
// The bool reports whether any stats record was found.
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

// AggregateStats merges every stats record in file order into one roll-up.
// Each record is a per-run delta, so token counts, calls and cost are summed;
// CostSource and Updated come from the newest record that carried them. This
// is what recovers a total across resumes.
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

// UsageRecords returns every usage record in append order, for cost auditing.
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
	// NaN/Inf cannot be encoded as JSON and would break the whole append.
	if st.CostUSD < 0 || math.IsNaN(st.CostUSD) || math.IsInf(st.CostUSD, 0) {
		st.CostUSD = 0
	}
	// A cost source is a closed set; dropping anything else also drops secrets
	// a caller may have put in this otherwise free-form field.
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
