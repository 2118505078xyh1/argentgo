package app

import (
	"strings"

	"example.com/argentgo/internal/session"
	"example.com/argentgo/internal/textutil"
)

// 本文件把压缩摘要的结构化 V2 投影接到续接摘要，作为 opt-in 的扩展段。
// 默认关闭时零调用、system 逐字节不变；开启时也只渲染"扩展字段"，与默认的
// 目标/改动文件/失败/下一步四段互补，绝不重复它们。

// summaryFieldsV2Env 是扩展字段续接段的独立开关：精确等于 "1" 才启用。
const summaryFieldsV2Env = "ARGENT_SUMMARY_FIELDS_V2"

// summaryFieldsV2Enabled 解释 ARGENT_SUMMARY_FIELDS_V2。nil env 或未设置时关闭。
func summaryFieldsV2Enabled(env func(string) string) bool {
	return env != nil && strings.TrimSpace(env(summaryFieldsV2Env)) == "1"
}

// BuildContinuationBriefV2 从线性会话条目构造一段"扩展字段"续接摘要。
//
// 它取最近一条可还原压缩记录的 StructuredV2 投影，只渲染扩展字段
// （Objective/Requirements/Decisions/Work State/Relevant Files/Important Context），
// 用 continuationBriefJoin 做有界拼接与去重，按固定顺序拼成中文标签行，
// 最后用 textutil.ClipRunes 钳总长（字簇安全）。
//
// 刻意不渲染 Progress/Uncertainties/UserActions：那三段由默认续接摘要负责，
// 避免两个 opt-in 同时开启时同一内容出现两次。没有扩展字段时返回空串。
// 纯函数：不读取全局状态、不修改 entries、输出确定。
func BuildContinuationBriefV2(entries []session.Entry, maxChars int) string {
	if len(entries) == 0 {
		return ""
	}
	if maxChars <= 0 {
		maxChars = continuationBriefDefaultMaxChars
	}

	var v2 session.StructuredSummaryV2
	found := false
	for i := len(entries) - 1; i >= 0; i-- {
		if c, ok := compactionFromEntry(entries[i]); ok {
			if parsed, ok := c.StructuredV2(); ok {
				v2 = parsed
				found = true
			}
			break
		}
	}
	if !found {
		return ""
	}

	join := func(items []string) string {
		return continuationBriefJoin(items, continuationBriefSectionRunes)
	}
	fields := []struct {
		label string
		value string
	}{
		{"目标", join(v2.Objective)},
		{"需求", join(v2.Requirements)},
		{"决策", join(v2.Decisions)},
		{"工作状态", join(v2.WorkState)},
		{"相关文件", join(v2.RelevantFiles)},
		{"重要上下文", join(v2.ImportantContext)},
	}

	var b strings.Builder
	b.WriteString("## 会话续接·扩展字段\n")
	wrote := false
	for _, f := range fields {
		if f.value == "" {
			continue
		}
		wrote = true
		b.WriteString("- " + f.label + ": " + f.value + "\n")
	}
	if !wrote {
		return ""
	}
	return textutil.ClipRunes(strings.TrimRight(b.String(), "\n"), maxChars)
}
