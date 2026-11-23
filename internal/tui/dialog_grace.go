package tui

import (
	"os"
	"strings"
	"time"
)

// defaultDialogGrace 是审批弹窗按键宽限期的产品默认值。弹窗刚出现的这
// 250ms 内吞掉尾随按键，避免操作者在提交工具调用后仍在进行的打字被误判为
// y/n/a 审批答复。保留 ARGENT_DIALOG_GRACE_MS=0 作为紧急回退。
const defaultDialogGrace = 250 * time.Millisecond

// resolveDialogGrace 计算宽限期默认值：只有环境变量修剪后精确等于 "0"
// 才关闭，其余情况一律使用产品默认值（用户零选择）。
func resolveDialogGrace() time.Duration {
	if m := dialogGraceEnv("ARGENT_DIALOG_GRACE_MS"); strings.TrimSpace(m) == "0" {
		return 0
	}
	return defaultDialogGrace
}

// dialogGraceEnv 是宽限期读取环境变量的窄接缝，便于测试注入而不依赖真实进程环境。
var dialogGraceEnv = os.Getenv

// DialogKeyGracePeriod 是审批对话框弹出后吞掉尾随按键的宽限期。
// 默认 250ms；唯一回退方式是把 ARGENT_DIALOG_GRACE_MS 设为 0，
// 或由集成方在启动前把本变量置 0。设置正值后，对话框出现后这段时间内的
// 按键会被吞掉；零值完全跳过判断，沿用即答即决的旧路径。
var DialogKeyGracePeriod = resolveDialogGrace()

// dialogGraceActive 报告在 now 时刻对话框是否仍处于宽限期。
// grace <= 0 直接返回 false，使关闭状态下的调用方走原有路径。
func dialogGraceActive(now, openedAt time.Time, grace time.Duration) bool {
	return grace > 0 && now.Sub(openedAt) < grace
}
