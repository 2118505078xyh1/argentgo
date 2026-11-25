package agent

import (
	"context"
	"fmt"
	"strings"

	"example.com/argentgo/internal/llm"
)

// 本文件把 TTSR 规则判定接到流式循环。核心约束：
//   - 默认（无规则 / watcher 为 nil）时 chatWithTTSR 就是 s.chat，逐字节不变；
//   - 命中 Warn/Inject 只发可观察事件，不改流、不改请求；
//   - 命中 Retry 才中止当前流、注入一条提醒用户消息、从同点重发一次（每轮最多一次）；
//   - 中止通过派生 ctx 的 cancel 实现，不触碰调用方的 ctx（外层取消语义不变）。

// newTTSRWatcherForRun 为本次运行构造观察器。规则为空时返回 nil，
// 使 chatWithTTSR 退化为直通（零成本、逐字节不变）。
func newTTSRWatcherForRun(rules []CompiledTTSRRule) *TTSRWatcher {
	if len(rules) == 0 {
		return nil
	}
	return NewTTSRWatcher(rules, 0, 0)
}

// chatWithTTSR 是一次模型请求的 TTSR 包装。它在 onDelta 之前把正文增量喂给观察器：
//
//  1. watcher 为 nil：直接 s.chat，历史行为逐字节不变；
//  2. Warn/Inject：发一条 EventNotice（文本带 [ttsr] 前缀），继续流式输出；
//  3. Retry（且本轮仍有预算）：
//     - 派生可取消 ctx，取消当前流；
//     - 把已流出的文本前缀留在 UI（不回退），但**不**把它写入 s.messages；
//     - 向 s.messages 追加一条用户角色的系统提醒（模型下一请求能看到）；
//     - reset 观察器后用同一 ctx 重发一次；重发输出只发布精确后缀（与首段前缀
//     对齐时去掉重复部分），避免同一段文本出现两次；
//     - 每轮最多重发一次，避免同一段输出反复触发。
//
// 返回的重发标志由调用方决定是否覆盖首次结果。注意：本函数只在 s.ttsr != nil 时
// 引入任何新行为；默认路径只有一次 nil 判断，不复制切片、不创建 ctx。
func (s *runState) chatWithTTSR(ctx context.Context, onDelta func(string)) (llm.Message, error) {
	if s.ttsr == nil {
		return s.chat(ctx, onDelta)
	}

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		retryVerdict *TTSRVerdict
		firstPrefix  strings.Builder
	)
	wrapped := func(delta string) {
		if onDelta != nil {
			onDelta(delta)
		}
		firstPrefix.WriteString(delta)
		if retryVerdict == nil {
			if v, ok := s.ttsr.Feed(TTSRScopeText, delta); ok {
				switch v.Action {
				case TTSRRetry:
					if s.ttsrRetries < maxTTSRRetriesPerTurn {
						cp := v
						retryVerdict = &cp
						cancel() // 中止当前流；外层 ctx 不受影响
					}
				case TTSRInject, TTSRWarn:
					s.emit(Event{Kind: EventNotice, Text: ttsrNoticeText(v)})
				}
			}
		}
	}

	reply, err := s.chat(streamCtx, wrapped)
	if retryVerdict == nil {
		return reply, err
	}

	// 命中 Retry：本次流已被取消，err 通常是 context.Canceled。把首段前缀保留在
	// s.messages 之外的 UI 事件里（onDelta 已发），但不写入历史，避免重复。
	firstText := firstPrefix.String()
	s.ttsrRetries++
	s.ttsr.Reset()
	s.emit(Event{Kind: EventNotice, Text: fmt.Sprintf(
		"[ttsr] 规则 %q 命中，已中止输出并注入提醒，从同点重试（%s）",
		retryVerdict.RuleName, boundedTTSRText(retryVerdict.Matched),
	)})
	// 提醒作为用户角色进入历史：下一请求能看到，且走安全链的只有工具调用，
	// 纯文本注入不会触发审批。文本有界（避免超长规则撑大上下文）。
	reminder := ttsrReminderText(retryVerdict.Reminder)
	s.messages = append(s.messages, llm.Message{Role: "user", Content: reminder})

	// 重发：这次喂一个本地捕获回调（不再触发 TTSR 取消），再只把"超出首段的精确
	// 后缀"发布给 UI，避免首段出现两次。这与流中断回补（emitRecoveredReply）同构。
	var resumed strings.Builder
	resumeReply, resumeErr := s.chat(ctx, func(delta string) {
		resumed.WriteString(delta)
	})
	if resumeErr != nil {
		// 重发失败：保留原失败路径，但历史里已注入提醒，下次调用会看到它。
		return resumeReply, resumeErr
	}
	publishText := resumed.String()
	if publishText == "" {
		publishText = resumeReply.Content
	}
	if onDelta != nil {
		if firstText != "" && strings.HasPrefix(publishText, firstText) {
			if suffix := strings.TrimPrefix(publishText, firstText); suffix != "" {
				onDelta(suffix)
			}
		} else if publishText != "" {
			onDelta(publishText)
		}
	}
	return resumeReply, resumeErr
}

// maxTTSRRetriesPerTurn 是一轮内 TTSR 触发重发的上限。一次即可：同一段若再次命中，
// 观察器已 reset，cooldown 清空，但预算用尽后不再重发，避免无限循环。
const maxTTSRRetriesPerTurn = 1

// ttsrNoticeText 把 Warn/Inject 裁决渲染为可观察事件文本。
func ttsrNoticeText(v TTSRVerdict) string {
	return fmt.Sprintf("[ttsr] 规则 %q 命中（%s）：%s",
		v.RuleName, v.Action, boundedTTSRText(v.Matched))
}

// ttsrReminderText 构造注入历史的提醒文本。为空时给一条通用提醒，确保模型
// 明确被告知"刚才的输出被规则中止"。
func ttsrReminderText(reminder string) string {
	if strings.TrimSpace(reminder) == "" {
		return "刚才的输出因触发了一条内容规则被中止。请忽略刚才那段内容，按用户原本的请求继续。"
	}
	return "刚才的输出因触发了一条内容规则被中止。请按以下提醒调整后继续：\n" + strings.TrimSpace(reminder)
}

// boundedTTSRText 把任意匹配文本截到 64 rune（CJK 安全），供事件文本使用。
func boundedTTSRText(s string) string {
	r := []rune(s)
	if len(r) > 64 {
		return string(r[:64]) + "…"
	}
	return s
}
