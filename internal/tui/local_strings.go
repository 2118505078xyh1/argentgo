package tui

import (
	"fmt"

	"example.com/argentgo/internal/i18n"
	"example.com/argentgo/internal/llm"
)

// localizedString 是一条用户可见文案的中英两栏。
type localizedString struct{ en, zh string }

// tuiLocalStrings 是产品打磨阶段新增文案的单一数据表。它们暂不进入
// internal/i18n 的主目录（该目录由另一路维护，且与本包解耦），因此在本包内
// 以一张表收集中英两栏；所有调用方统一走 tuiText / tuiTextf，不在代码里
// 写死英文短句，从而不增长 i18n 的英文硬编码基线。
var tuiLocalStrings = map[string]localizedString{
	"quit_armed":       {en: "press Ctrl+C again to quit", zh: "再按一次 Ctrl+C 退出"},
	"approval_queue":   {en: "%d more pending", zh: "还有 %d 个待确认"},
	"allow_exact":      {en: "allow this call only", zh: "仅此调用不再询问"},
	"approved_exact":   {en: "allowed this call", zh: "已允许此调用"},
	"hint_modal_local": {en: "y allow · n deny · a allow this call only · enter confirm default · esc deny", zh: "y 允许 · n 拒绝 · a 仅此调用不再询问 · enter 确认默认项 · esc 拒绝"},
	"error_action":     {en: "retry: /retry", zh: "重试：/retry"},
	"error_expand":     {en: "[+] details", zh: "[+] 详情"},
	"error_collapse":   {en: "[-] hide", zh: "[-] 收起"},
	"retry_line":       {en: "⟳ retry %s in %dms (%d/%d)", zh: "⟳ 重试 %s，%dms 后（第%d/%d次）"},
	"goal_status":      {en: "goal loop: %s (%d/%d)", zh: "目标循环：%s（第 %d/%d 次）"},
}

// goalStatusStateLocal 是 /goal 状态的封闭枚举短句。未知状态由 localGoalState
// 有界透传，绝不把整段 JSON 或自由格式文本带进界面。
var goalStatusStateLocal = map[string]localizedString{
	"running":     {en: "running", zh: "运行中"},
	"complete":    {en: "complete", zh: "已完成"},
	"capped":      {en: "capped", zh: "已达上限"},
	"interrupted": {en: "interrupted", zh: "已中断"},
}

// localGoalState 把封闭状态标记本地化；未知标记折叠并限长后原样返回，
// 既不会漏显示新状态，也不会被异常生产者撑宽 transcript。
func localGoalState(locale, state string) string {
	if s, ok := goalStatusStateLocal[state]; ok {
		if language(locale) == i18n.ZhCN {
			return s.zh
		}
		return s.en
	}
	return Truncate(collapseSpace(llm.RedactText(state)), 32)
}

// retryReasonLocal 把 agent/llm 发出的封闭 reason 标记映射成短句。
// 未知标记原样返回（它已经过脱敏与折叠），不会被本表改写。
var retryReasonLocal = map[string]localizedString{
	"":             {en: "stream", zh: "请求失败"},
	"stream":       {en: "stream", zh: "流式错误"},
	"status":       {en: "server busy", zh: "服务繁忙"},
	"not_sent":     {en: "not sent", zh: "请求未发出"},
	"unreachable":  {en: "unreachable", zh: "网络不可达"},
	"timeout":      {en: "timeout", zh: "请求超时"},
	"overloaded":   {en: "overloaded", zh: "服务过载"},
	"rate_limit":   {en: "rate limit", zh: "触发限流"},
	"sse reset":    {en: "connection reset", zh: "连接中断"},
	"stream reset": {en: "connection reset", zh: "连接中断"},
}

// tuiText 返回本包文案表中 key 的本地化文本。未知 key 返回空串，
// 让调用方立刻暴露漏登记，而不是把英文键名泄漏到界面上。
func tuiText(locale, key string) string {
	s, ok := tuiLocalStrings[key]
	if !ok {
		return ""
	}
	if language(locale) == i18n.ZhCN {
		return s.zh
	}
	return s.en
}

// tuiTextf 是 tuiText 的格式化包装，供带计数的文案使用。
func tuiTextf(locale, key string, args ...any) string {
	return fmt.Sprintf(tuiText(locale, key), args...)
}

// localRetryReason 把封闭的重试原因标记映射成本地化短句。
func localRetryReason(locale, reason string) string {
	s, ok := retryReasonLocal[reason]
	if !ok {
		return reason
	}
	if language(locale) == i18n.ZhCN {
		return s.zh
	}
	return s.en
}
