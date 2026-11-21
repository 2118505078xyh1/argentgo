package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/provider"
	"github.com/2118505078xyh1/argentgo/internal/session"
	"github.com/2118505078xyh1/argentgo/internal/tools"
)

// contextFilesNote 在文件内容占用为 0 时打印：CLI 没有活动上下文提示，
// 无法把工作区文件映射到本次请求实际携带的内容，因此明确说明而不臆测计数。
const contextFilesNote = "说明：CLI 无活动提示，文件内容占用未计入（恒为 0）。"

// ContextReport 渲染 `argentgo context` 的上下文占用明细。
//
// 它是只读的：系统与工具 schema 沿用 TokenReport 的同一口径；历史消息只从
// 已持久化的会话估算（无 --session 时取最新会话），打开失败或没有会话时返回
// 0，绝不臆测。文件内容恒为 0，并在输出中说明原因。整个过程不创建会话、
// 不写入配置也不调用提供方。模型名与分解视图中的中文分类均保持原始 UTF-8，
// 表格对齐交给 cli.FormatContextBreakdown 处理。
func ContextReport(stdio cli.IO, opts cli.Options) (string, error) {
	res, err := Resolve(stdio, opts)
	if err != nil {
		return "", err
	}

	specs := tools.NewWithLimit(res.Root, res.Mutating, 0).Specs()
	systemTokens := agent.EstimateTokens(res.System)
	schemaTokens := agent.EstimateTools(specs)
	historyTokens := contextHistoryTokens(res)
	// CLI 没有活动上下文提示，无法把任何文件计入本次请求的 token。
	filesTokens := 0

	window := res.Cfg.ContextWindow
	if window <= 0 {
		// 与 compactionInputs 同一回落链：显式配置 > 模型/模板推导。
		window = provider.ContextWindowFor(res.Provider, res.Model, 0)
	}

	segs := cli.BreakdownContext(systemTokens, schemaTokens, historyTokens, filesTokens)
	var b strings.Builder
	fmt.Fprintf(&b, "model: %s (%s)\n", res.Model, res.Protocol)
	b.WriteString(cli.FormatContextBreakdown(segs))
	// ContextHealth 不带行尾换行，这里补上，保证说明行（及整段输出）从新行开始。
	fmt.Fprintln(&b, cli.ContextHealth(segs, window))
	if filesTokens == 0 {
		fmt.Fprintln(&b, contextFilesNote)
	}
	return b.String(), nil
}

// contextHistoryTokens 从持久化会话估算历史消息占用的 token。
//
// 无 --session 时使用 store.List 返回的最新会话；显式 --session 走与恢复同一条
// 路径（sessionIDFromArg），使 id、唯一前缀和存储内的会话文件路径都能被识别。
// 只累加非 tool 角色的 Content；打开失败、没有会话或读取失败都返回 0。
// 目录不存在时直接返回 0，不通过 session.Open 创建它，从而保持严格只读。
func contextHistoryTokens(res Resolved) int {
	dir := filepath.Join(res.Storage, "sessions")
	if _, err := os.Stat(dir); err != nil {
		return 0
	}
	store, err := session.Open(dir)
	if err != nil {
		return 0
	}

	id := strings.TrimSpace(res.Options.Session)
	if id == "" {
		metas, err := store.List()
		if err != nil || len(metas) == 0 {
			return 0
		}
		id = metas[0].ID
	} else {
		id = sessionIDFromArg(store, id)
	}

	sess, err := store.OpenSession(id)
	if err != nil {
		return 0
	}
	total := 0
	for _, entry := range sess.Messages() {
		if entry.Role == "tool" {
			continue
		}
		total += agent.EstimateTokens(entry.Content)
	}
	return total
}
