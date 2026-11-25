package app

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"example.com/argentgo/internal/session"
)

// modelUsage 是某个模型在会话的逐次请求用量记录中的聚合份额。
// 模型名被原样保留（包括 CJK 字符），使报告能指出实际服务这些请求的模型。
type modelUsage struct {
	Model  string
	Input  int
	Output int
	Cached int
	Calls  int
}

// cumulativeSessionUsage 是 session 包累积 API 的生产读取方。
// AggregateStats 提供跨恢复的 token/花费汇总，UsageRecords 从原始逐次请求事实中提供按模型的细分。
// 没有统计记录的会话产生零总量；没有用量记录的会话产生空的细分。两者都是有效状态，而非错误。
//
// 这个 app 侧消费者让 session.AggregateStats/UsageRecords 拥有真实的生产调用路径：
// 没有它，这些 API 只会被 session 自身的测试触及，
// 一个已恢复会话报告的用量可能静默地是上一次运行的值，而非累积总量。
func cumulativeSessionUsage(sess *session.Session) (session.SessionStats, []modelUsage) {
	if sess == nil {
		return session.SessionStats{}, nil
	}
	total := sess.AggregateStats()

	byModel := make(map[string]*modelUsage)
	for _, rec := range sess.UsageRecords() {
		model := rec.Model
		if model == "" {
			model = "(unknown)"
		}
		entry := byModel[model]
		if entry == nil {
			entry = &modelUsage{Model: model}
			byModel[model] = entry
		}
		entry.Input += rec.Input
		entry.Output += rec.Output
		entry.Cached += rec.Cached
		entry.Calls++
	}

	out := make([]modelUsage, 0, len(byModel))
	for _, entry := range byModel {
		out = append(out, *entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return total, out
}

// appendSessionCumulative 向 --tokens 输出添加一个跨恢复的用量块。
// 它只在 --session 指向某个会话时运行，因此普通的 `--tokens` 调用保持其原有输出不变，
// 且不会改变任何 agent 事件或协议形状。
func appendSessionCumulative(b *strings.Builder, res Resolved) error {
	id := strings.TrimSpace(res.Options.Session)
	if id == "" {
		return nil
	}
	store, err := session.Open(filepath.Join(res.Storage, "sessions"))
	if err != nil {
		return fmt.Errorf("open session store: %w", err)
	}
	sess, err := store.OpenSession(sessionIDFromArg(store, id))
	if err != nil {
		return fmt.Errorf("resume session %q: %w", id, err)
	}
	total, models := cumulativeSessionUsage(sess)

	fmt.Fprintf(b, "\nsession %s cumulative (persisted stats):\n", sess.ID())
	fmt.Fprintf(b, "  prompt=%d completion=%d cached=%d calls=%d cost=$%.4f",
		total.PromptTokens, total.CompletionTokens, total.CachedTokens, total.Calls, total.CostUSD)
	if total.CostSource != "" {
		fmt.Fprintf(b, " (%s)", total.CostSource)
	}
	fmt.Fprintln(b)
	if len(models) == 0 {
		fmt.Fprintln(b, "  models: (no usage records)")
		return nil
	}
	for _, m := range models {
		fmt.Fprintf(b, "  model[%s]: input=%d output=%d cached=%d calls=%d\n",
			m.Model, m.Input, m.Output, m.Cached, m.Calls)
	}
	return nil
}
