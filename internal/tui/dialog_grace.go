package tui

import "time"

// DialogKeyGracePeriod 是审批对话框弹出后吞掉尾随按键的宽限期。
//
// 默认 0 表示关闭：按键依旧立即被当作审批回答，现有行为逐字节不变。
// 设置正值（例如 300ms）后，对话框出现后这段时间内的按键会被吞掉，
// 避免操作者在提交工具调用后仍在进行的打字被误判为 y/n/a 审批答复。
//
// 这是纯 TUI 能力：是否启用完全由集成方决定，本包不主动接线。
var DialogKeyGracePeriod time.Duration

// dialogGraceActive 报告在 now 时刻对话框是否仍处于宽限期。
// grace <= 0 直接返回 false，使关闭状态下的调用方走原有路径。
func dialogGraceActive(now, openedAt time.Time, grace time.Duration) bool {
	return grace > 0 && now.Sub(openedAt) < grace
}
