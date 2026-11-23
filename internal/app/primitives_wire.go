package app

// 本文件把各并行代理留下的会话原语真实接入生产路径（默认开启、零用户选择）。
// 接线汇聚在 app 层：agent 保持对 session/tools/config 零依赖，所有会话侧能力
// 都经由 agent.Options 的接缝或 ToolRunner 装饰器注入。每个辅助函数在无相关
// 数据时都是空操作，因此干净会话与接线前逐字节一致。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/session"
)

// imageOffloadKeep 是下一轮投影保留的最近图像数量上限。
// 超过这个数量的最旧图像 occurrence 会被写入卸载标记，投影为文本占位。
const imageOffloadKeep = 8

// claimToolNames 是领取账本覆盖的非幂等工具集合，与 agent 的默认审批集合对齐。
// 未列出的工具一律只读或幂等，不写领取记录。
var claimToolNames = map[string]bool{
	"write": true, "edit": true, "multi_edit": true, "apply_patch": true,
	"patch": true, "delete": true, "remove": true, "move": true, "copy": true,
	"mkdir": true, "bash": true, "shell": true, "exec": true, "run": true,
	"memory_consolidate": true, "save_skill": true,
}

// wireOmissionNotice 把「缺失不构成授权」告示作为下一轮用户消息注入 hook 上下文队列。
// 干净会话的 MessagesWithOmissionNotice 与 Messages() 逐字节相同，因此这里零注入。
func wireOmissionNotice(q *agent.HookContextQueue, sess *session.Session) {
	if q == nil || sess == nil {
		return
	}
	for _, entry := range sess.MessagesWithOmissionNotice() {
		if entry.ID == session.OmissionNoticeID {
			q.Add(entry.Content)
			return
		}
	}
}

// maybeOffloadImages 在图像超出保留上限时，把最旧的那些 occurrence 写入卸载标记。
// 标记是记录驱动的投影：原始转录字节永不改动；无图像或图像数在上限内时零写入。
func maybeOffloadImages(sess *session.Session) {
	if sess == nil {
		return
	}
	records, err := sess.Records()
	if err != nil {
		return
	}
	existing := session.ImageOffloadsIn(records)
	entries := sess.Messages()
	// 请求足够多的标记以得到全部未卸载 occurrence（len(entries)+1 必然覆盖上限）。
	plan := session.PlanImageOffload(entries, existing, len(entries)+1)
	excess := len(plan) - imageOffloadKeep
	if excess <= 0 {
		return
	}
	for _, marker := range plan[:excess] {
		if err := sess.AppendImageOffload(marker.Seq, marker.Index); err != nil {
			return
		}
	}
}

// toolClaimKey 为非幂等工具调用生成稳定且有界的领取标识。
// 它只依赖工具名与规范化后的参数字节，因此崩溃重放能命中同一条领取记录。
func toolClaimKey(name, args string) string {
	sum := sha256.Sum256([]byte(name + "\x00" + args))
	return "claim-" + hex.EncodeToString(sum[:])
}

// claimingTools 在 ToolRunner 边界实现领取账本：每次非幂等调用执行前都落盘领取
// 记录（默认开启）；仅当会话确有恢复公告时 enforce 置位，此时重放命中会跳过执行
// 并经 ResultForClaim 回灌原结果。干净运行只写账本、不触发跳过，避免改变正常的
// 重复调用语义，同时保证崩溃前的副作用在之后恢复时仍能被识别。
type claimingTools struct {
	inner   agent.ToolRunner
	sess    *session.Session
	enforce bool
}

// Specs 原样转发工具目录，模型看到的清单不因领取账本改变。
func (c *claimingTools) Specs() []llm.ToolSpec {
	return c.inner.Specs()
}

// Run 在真正执行非幂等工具前检查并写入领取记录。
func (c *claimingTools) Run(ctx context.Context, name, args string) (string, error) {
	if c == nil || c.inner == nil || c.sess == nil || !claimToolNames[name] {
		return c.inner.Run(ctx, name, args)
	}
	key := toolClaimKey(name, args)
	if c.enforce {
		if state, err := c.sess.ToolClaims(); err == nil && session.ShouldSkipToolCall(state, key) {
			if result, ok := session.ResultForClaim(state, key); ok {
				return result, nil
			}
			// 没有可回灌的原结果时也不重放副作用：给模型一条可读的跳过说明。
			return "[claim] non-idempotent tool already executed; skipped replay to avoid repeated side effects", nil
		}
	}
	// 账本不可写时 TryAppendToolClaim 内部记 warn 并继续，绝不阻断工具执行。
	c.sess.TryAppendToolClaim(key)
	out, err := c.inner.Run(ctx, name, args)
	if err == nil {
		// 把结果写回领取记录，供重放时经 ResultForClaim 原样回灌。
		_, _ = c.sess.AppendRecord(session.KindToolClaim, session.ToolClaim{ToolCallID: key, Result: out})
	}
	return out, err
}
