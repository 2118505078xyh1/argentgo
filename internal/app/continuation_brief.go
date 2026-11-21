package app

import (
	"encoding/json"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/session"
	"github.com/2118505078xyh1/argentgo/internal/textutil"
)

// 本文件提供 R23「会话续接摘要」（continuation brief）的纯函数原语，刻意保持零调用方：
// 不读取进程环境、不注册回调、不触碰 Execute/压缩写入路径。默认关闭时现有行为逐字节不变；
// 接线（env 门控 + 把摘要拼入下一轮提示）由主会话在 opt-in 开关开启时显式完成。
//
// 设计约束（与 R23 学习保持一致）：
//   - 复用 session.Compaction.Structured() 的只读投影，不重新实现 markdown 解析；
//   - 权威机器字段（Files/Failures/NextSteps）优先，结构化投影仅作补充/回退；
//   - 用 textutil.ClipRunes 做字簇安全的有界截断（CJK/emoji/组合符不被劈开）；
//   - 纯函数：不改入参、顺序稳定、输出确定性。

const (
	// continuationBriefDefaultMaxChars 是 maxChars<=0 时使用的默认长度上界（按 rune 计）。
	continuationBriefDefaultMaxChars = 2000
	// continuationBriefSectionRunes 是单个字段（目标/文件/失败/下一步）的 rune 上界。
	// 它保证某一段异常冗长时不会独占整个摘要；总长仍由 maxChars 最终钳制。
	continuationBriefSectionRunes = 600
	// continuationBriefGoalFallbackRunes 是回退到最近 user 消息时首行的 rune 上界。
	continuationBriefGoalFallbackRunes = 200
	// continuationBriefEnvKey 是续接摘要开关的环境变量键名。
	continuationBriefEnvKey = "ARGENT_CONTINUATION_BRIEF"
)

// continuationBriefEnabled 报告续接摘要是否开启。env 为 nil、变量缺失或值非 "1" 时
// 一律按默认关闭处理（fail-closed），因此未装配环境的调用方得不到任何行为变化。
func continuationBriefEnabled(env func(string) string) bool {
	if env == nil {
		return false
	}
	return strings.TrimSpace(env(continuationBriefEnvKey)) == "1"
}

// continuationBriefParts 是续接摘要的四个有界字段。
type continuationBriefParts struct {
	goal      string
	files     string
	failures  string
	nextSteps string
}

// empty 报告四段是否全为空。
func (p continuationBriefParts) empty() bool {
	return p.goal == "" && p.files == "" && p.failures == "" && p.nextSteps == ""
}

// continuationBriefJoin 把若干条目拼成一个字段值：去空白、跳过空项与占位符、
// 去重、保留原有顺序，按 maxRunes 做字簇安全的有界截断，最后以全角分号连接。
// 它只读取 items，不修改入参；去重用的 map 只影响查找，不影响输出顺序。
func continuationBriefJoin(items []string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	seen := make(map[string]bool, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		v := strings.TrimSpace(item)
		if v == "" || v == "(none)" || v == "（无）" || v == "无" {
			continue
		}
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	if len(out) == 0 {
		return ""
	}
	return textutil.ClipRunes(strings.Join(out, "；"), maxRunes)
}

// compactionFromEntry 尝试把一条 Entry 还原成压缩载荷。session.(*Session).Messages()
// 把最近的 KindCompaction 记录投影为 Role="system"、Data 为该压缩 JSON 的 Entry；据此识别。
// 只有确实带有摘要文本或权威字段的记录才算数，避免把普通 system 消息误判为压缩记录。
func compactionFromEntry(e session.Entry) (session.Compaction, bool) {
	if e.Role != "system" || len(e.Data) == 0 {
		return session.Compaction{}, false
	}
	var c session.Compaction
	if json.Unmarshal(e.Data, &c) != nil {
		return session.Compaction{}, false
	}
	if c.Summary == "" && len(c.Files) == 0 && len(c.Failures) == 0 && len(c.NextSteps) == 0 {
		return session.Compaction{}, false
	}
	return c, true
}

// continuationBriefFromCompaction 从压缩载荷抽取四个字段。
//
// 目标取自 Structured() 的 Progress 桶首条：Summarize 总是先写 "## Goal" 章节，
// 而 ParseStructuredSummary 把 "goal" 映射到 Progress，因此首条即本轮目标。
// Failures/NextSteps 优先用 Compaction 上的权威机器字段，缺失时回退到 Structured()
// 的对应桶（Failures→Uncertainties，NextSteps→UserActions）。Files 只有结构上的权威字段。
// Structured() 是只读投影；解析失败时忽略 ok，仍可用权威字段拼出摘要。
func continuationBriefFromCompaction(c session.Compaction) continuationBriefParts {
	structured, _ := c.Structured()

	var p continuationBriefParts
	if len(structured.Progress) > 0 {
		p.goal = continuationBriefJoin(structured.Progress[:1], continuationBriefSectionRunes)
	}
	p.files = continuationBriefJoin(c.Files, continuationBriefSectionRunes)
	if p.failures = continuationBriefJoin(c.Failures, continuationBriefSectionRunes); p.failures == "" {
		p.failures = continuationBriefJoin(structured.Uncertainties, continuationBriefSectionRunes)
	}
	if p.nextSteps = continuationBriefJoin(c.NextSteps, continuationBriefSectionRunes); p.nextSteps == "" {
		p.nextSteps = continuationBriefJoin(structured.UserActions, continuationBriefSectionRunes)
	}
	return p
}

// continuationFirstLine 取文本首行并按 rune 有界（CJK/emoji 安全）。
func continuationFirstLine(s string, maxRunes int) string {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return ""
	}
	line := trimmed
	if i := strings.IndexAny(trimmed, "\r\n"); i >= 0 {
		line = strings.TrimSpace(trimmed[:i])
	}
	return textutil.ClipRunes(line, maxRunes)
}

// continuationBriefFromUser 在无可结构化摘要（无压缩记录或摘要格式漂移）时，
// 回退到最近一条 user 消息，用其首行（有界）作为目标，其余三段留空。
func continuationBriefFromUser(entries []session.Entry) continuationBriefParts {
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Role == "user" {
			if goal := continuationFirstLine(entries[i].Content, continuationBriefGoalFallbackRunes); goal != "" {
				return continuationBriefParts{goal: goal}
			}
		}
	}
	return continuationBriefParts{}
}

// BuildContinuationBrief 从线性会话条目构造一段跨 Execute 边界的续接摘要。
// 它是纯函数：不读取全局状态、不修改 entries，输出顺序稳定且确定性。
//
// 流程：
//  1. 从后往前找最近的可还原压缩记录（KindCompaction），抽取目标/文件/失败/下一步；
//  2. 若四段全空（无压缩或摘要格式漂移），回退到最近 user 消息的首行作为目标；
//  3. 以固定中文标签拼成四行，最后用 textutil.ClipRunes 按 maxChars 钳总长（字簇安全）。
//
// nil/空 entries 返回空串；maxChars<=0 时使用 continuationBriefDefaultMaxChars。
// 四段固定顺序、各出现一次；空段以 "（无）" 占位，保证格式稳定。
func BuildContinuationBrief(entries []session.Entry, maxChars int) string {
	if len(entries) == 0 {
		return ""
	}
	if maxChars <= 0 {
		maxChars = continuationBriefDefaultMaxChars
	}

	parts := continuationBriefParts{}
	for i := len(entries) - 1; i >= 0; i-- {
		if c, ok := compactionFromEntry(entries[i]); ok {
			parts = continuationBriefFromCompaction(c)
			break
		}
	}
	if parts.empty() {
		parts = continuationBriefFromUser(entries)
	}
	if parts.empty() {
		return ""
	}

	const none = "（无）"
	value := func(s string) string {
		if s == "" {
			return none
		}
		return s
	}

	var b strings.Builder
	b.WriteString("## 会话续接\n")
	b.WriteString("- 目标: " + value(parts.goal) + "\n")
	b.WriteString("- 改动文件: " + value(parts.files) + "\n")
	b.WriteString("- 失败: " + value(parts.failures) + "\n")
	b.WriteString("- 下一步: " + value(parts.nextSteps))
	return textutil.ClipRunes(b.String(), maxChars)
}
