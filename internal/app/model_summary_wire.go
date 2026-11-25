package app

import (
	"context"
	"strings"
	"time"

	"example.com/argentgo/internal/agent"
	"example.com/argentgo/internal/llm"
	"example.com/argentgo/internal/session"
)

// modelSummaryTimeout 是模型摘要调用的硬上限。压缩发生在上下文压力最高时，
// 超时立即回落抽取式，绝不阻塞主循环。
const modelSummaryTimeout = 3 * time.Second

// applyModelSummaryWireWithLocalTrim 是模型摘要接线的「本地裁剪优先」包装。
// 它在 applyModelSummaryWire 之前插入一次纯估算：当 localTrimFirst opt-in 开启且
// 被丢弃条目的 head+tail 裁剪已省够时，直接返回抽取式摘要，跳过模型调用（省一次成本）。
//
// dropped 是压缩切点之前的被丢弃条目；raw 是合并配置，用于读取 opt-in 与裁剪预算。
// 默认（opt-in 关闭、model_summary 关闭、省不够）一律委托 applyModelSummaryWire，
// 因此默认帧 / 事件 / 摘要逐字节不变。估算只读 dropped，不修改入参，可并发调用。
func applyModelSummaryWireWithLocalTrim(
	extractive string,
	dropped []session.Entry,
	raw map[string]any,
	reg *OptInRegistry,
	ctx context.Context,
	chatter llm.Chatter,
	summaryModel string,
	failures, nextSteps, files []string,
	emit func(agent.Event),
) string {
	if shouldSkipModelSummaryForLocalTrim(dropped, raw, reg) {
		return extractive
	}
	return applyModelSummaryWire(extractive, reg, ctx, chatter, summaryModel, failures, nextSteps, files, emit)
}

// applyModelSummaryWire 是模型摘要（opt-in model_summary）在压缩点的统一接线点。
//
// 门控（任一不满足即原样返回 extractive，零调用、零事件）：
//   - reg 为 nil 或 model_summary 未开启；
//   - summaryModel 为空（约定用 secondary-model 发起，为空即不调用，控制成本）；
//   - chatter 为 nil。
//
// 成功时用模型生成的增量叙事替换摘要（ApplyModelSummary 保证权威字段在标记之前、
// 逐字节保留）；任何失败（ctx 取消/超时/Chat 报错/坏 JSON）静默回落抽取式。
//
// 成本可见：成功时经 emit 发一个 EventUsage（当轮用量），使 SSE/RPC/TUI/usage 工具
// 能看到这次旁路调用。注意该开销**不计入 agent 预算与 session stats**（它绕过 agent
// 循环，不经过 account()）；文档已明确此边界。
func applyModelSummaryWire(
	extractive string,
	reg *OptInRegistry,
	ctx context.Context,
	chatter llm.Chatter,
	summaryModel string,
	failures, nextSteps, files []string,
	emit func(agent.Event),
) string {
	if !modelSummaryEnabled(reg) || strings.TrimSpace(summaryModel) == "" || chatter == nil {
		return extractive
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx2, cancel := context.WithTimeout(ctx, modelSummaryTimeout)
	defer cancel()

	narrative, steps, usage, err := GenerateModelSummaryWithUsage(ctx2, chatter, summaryModel, ModelSummaryInput{
		Extractive: extractive,
		Failures:   failures,
		NextSteps:  nextSteps,
		Files:      files,
		MaxChars:   8000,
	})
	if err != nil {
		return extractive // 失败回落：不发事件、不改摘要
	}
	out := ApplyModelSummary(extractive, narrative, steps)
	if out == extractive {
		return out // 无增量：不发事件
	}
	if emit != nil {
		emit(agent.Event{Kind: agent.EventUsage, Usage: usage, TotalUsage: usage})
	}
	return out
}
