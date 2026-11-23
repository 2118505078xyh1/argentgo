package app

import (
	"context"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/hooks"
)

// hookLifecycleEnv 是「生命周期事件映射到 hook」的独立门控变量。
const hookLifecycleEnv = "ARGENT_HOOK_LIFECYCLE"

// hookLifecycleEnabled 报告是否把 agent 事件映射到 hook。默认开启，仅当
// ARGENT_HOOK_LIFECYCLE=0（允许首尾空白）时紧急关闭。
func hookLifecycleEnabled(env func(string) string) bool {
	return env == nil || strings.TrimSpace(env(hookLifecycleEnv)) != "0"
}

// lifecycleHookEvent 把 agent 事件映射为对应的 hook 事件；无对应时返回 ok=false。
// EventStop 按其 Reason 分类：cancelled → Interrupt，provider_error/其它错误 →
// StopFailure，其余（含 complete/max_turns）→ TurnEnd。
func lifecycleHookEvent(event agent.Event) (hooks.Event, bool) {
	switch event.Kind {
	case agent.EventTurnStart:
		return hooks.TurnStart, true
	case agent.EventTurnEnd:
		return hooks.EventTurnEnd, true
	case agent.EventSubagentEnd:
		return hooks.SubagentStop, true
	case agent.EventStop:
		switch event.Reason {
		case agent.StopCancelled:
			return hooks.EventInterrupt, true
		case agent.StopProviderError:
			return hooks.EventStopFailure, true
		default:
			return hooks.EventTurnEnd, true
		}
	}
	return "", false
}

// fireLifecycleHook 在门控开启时把事件投递给 hook bridge。
// bridge 为 nil 或事件无对应 hook 时是空操作。
func fireLifecycleHook(bridge *hookBridge, enabled bool, event agent.Event) {
	if !enabled || bridge == nil {
		return
	}
	he, ok := lifecycleHookEvent(event)
	if !ok {
		return
	}
	payload := hooks.Payload{HookPayload: event.Reason}
	if event.Text != "" {
		payload.Output = event.Text
	}
	_, _ = bridge.fire(context.Background(), he, payload)
}
