package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/session"
)

// 本文件提供"失败尝试隔离"的 app 侧装配骨架。它刻意保持零调用方：不读取进程环境、
// 不注册任何回调、不碰 configureSubagents（那是下一步的接线工作）。唯一职责是提供两个
// 可被注入的函数，使后续接线只需一行调用即可启用，而默认关闭时现有运行逐字节不变。

// attemptIsolationNoticePrefix 是隔离通知的固定前缀。所有由本文件发出的 EventNotice
// 都以它开头，便于 UI/测试稳定识别，且不会与验证循环自身的 "[verify: ...]" 通知混淆。
const attemptIsolationNoticePrefix = "[attempt isolation] "

// attemptIsolationOn 报告失败尝试隔离是否开启。reg 为 nil 时按默认关闭处理（fail-closed），
// 因此零值注册表与未装配注册表的调用方都得不到任何行为变化。
func attemptIsolationOn(reg *OptInRegistry) bool {
	if reg == nil {
		return false
	}
	return reg.Enabled(OptInAttemptIsolation)
}

// wireVerifyFail 构造一个可注入 SubagentConfig.OnVerifyFail 的回调。
//
// 关闭（reg 为 nil、开关未开启，或 sess 为 nil）时返回 nil，调用方据此不装配回调，
// 主循环行为与事件流完全不变。开启时返回的闭包只做两件事：把失败尝试分叉成一个旁支
// 会话，并通过 EmitEvent 追加一条携带旁支 id 的 EventNotice。
//
// 关键约定：任何错误（包括空的 AttemptID）都只转成一条 notice，闭包始终返回 nil。
// 这样隔离机制是纯观察/旁支性质的，绝不会改变验证重试或任何其他循环决策。
// 没有事件桥接器附着在 ctx 上时 EmitEvent 是空操作，与 app 其他通知保持一致。
// wireAttemptRoot 构造可注入 SubagentConfig.BeginAttempt/EndAttempt 的两个闭包，
// 由它们把真实会话记录 id 带进验证失败信息。与 wireVerifyFail 共用 attempt_isolation
// 门控：关闭（reg 为 nil 或开关未开，或 sess 为 nil）时返回两个 nil，调用方不装配，
// runTaskVerified 中的新分支全部跳过，AttemptID 仍为原合成串，逐字节不变。
func wireAttemptRoot(sess *session.Session, reg *OptInRegistry) (func(string) (string, error), func(string, string) error) {
	if sess == nil || !attemptIsolationOn(reg) {
		return nil, nil
	}
	return func(reason string) (string, error) {
			return session.BeginAttempt(sess, reason)
		},
		func(rootID, verdict string) error {
			return session.EndAttempt(sess, rootID, verdict)
		}
}

func wireVerifyFail(sess *session.Session, reg *OptInRegistry) func(ctx context.Context, info agent.VerifyFailInfo) error {
	if sess == nil || !attemptIsolationOn(reg) {
		return nil
	}
	return func(ctx context.Context, info agent.VerifyFailInfo) error {
		attemptID := strings.TrimSpace(info.AttemptID)
		if attemptID == "" {
			EmitEvent(ctx, agent.Event{Kind: agent.EventNotice,
				Text: attemptIsolationNoticePrefix + "empty attempt id; nothing to isolate"})
			return nil
		}

		fork, err := sess.DiscardFailedAttempt(attemptID)
		if err != nil {
			EmitEvent(ctx, agent.Event{Kind: agent.EventNotice,
				Text: fmt.Sprintf("%sdiscard failed attempt %q failed: %v", attemptIsolationNoticePrefix, attemptID, err)})
			return nil
		}
		if fork == nil {
			EmitEvent(ctx, agent.Event{Kind: agent.EventNotice,
				Text: fmt.Sprintf("%sdiscard failed attempt %q produced no fork", attemptIsolationNoticePrefix, attemptID)})
			return nil
		}

		EmitEvent(ctx, agent.Event{Kind: agent.EventNotice,
			Text: fmt.Sprintf("%sfailed attempt %q isolated into fork session %s", attemptIsolationNoticePrefix, attemptID, fork.ID())})
		return nil
	}
}
