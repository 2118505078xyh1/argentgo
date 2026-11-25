package app

import (
	"encoding/json"
	"fmt"
	"strings"

	"example.com/argentgo/internal/session"
	"example.com/argentgo/internal/tools"
)

// historySource 把会话的 JSONL 记录适配为消费侧的 tools.HistorySource 接口。
// 它位于 app 中，使 internal/tools 无需导入 internal/session（分层规则）。
type historySource struct {
	sess *session.Session
}

// Search 为当前活动会话返回一段紧凑、有界的匹配记录行字符串。空查询返回最近的行。
func (h *historySource) Search(query string, limit int) string {
	if h == nil || h.sess == nil {
		return ""
	}
	if limit <= 0 {
		limit = 20
	}
	const capLimit = 100
	if limit > capLimit {
		limit = capLimit
	}
	hits, err := session.Search(h.sess.Dir(), query, limit)
	if err != nil || len(hits) == 0 {
		return "no matching history"
	}
	type entry struct {
		Line    int    `json:"line"`
		Snippet string `json:"snippet"`
	}
	out := make([]entry, 0, len(hits))
	for _, hit := range hits {
		snippet := hit.Snippet
		// 折叠换行并限制每个片段的长度，使工具输出保持紧凑。
		snippet = strings.Join(strings.Fields(snippet), " ")
		if len(snippet) > 240 {
			snippet = snippet[:240] + "…"
		}
		out = append(out, entry{Line: hit.Line, Snippet: snippet})
	}
	data, err := json.Marshal(out)
	if err != nil {
		return fmt.Sprintf("history: %v", err)
	}
	return string(data)
}

// wireHistory 在已知会话之后，把 history 工具挂接到注册表。
func wireHistory(registry *tools.Registry, sess *session.Session) {
	if registry == nil || sess == nil {
		return
	}
	registry.SetHistorySource(&historySource{sess: sess})
}
