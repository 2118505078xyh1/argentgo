package app

import (
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/agent"
)

// loopDetectionEnableEnv 是工具调用循环检测的开关。
// 默认开启；仅当值为假值词（0 / false / no / off，忽略大小写与首尾空白）时才关闭。
// 未设置、空值或其它值一律视为开启。
const loopDetectionEnableEnv = "ARGENT_LOOP_DETECTION"

// loopDetectionEnabledFromEnv 报告本次运行的开关是否开启（默认开，假值词关闭）。
func loopDetectionEnabledFromEnv(env func(string) string) bool {
	if env == nil {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(env(loopDetectionEnableEnv))) {
	case "0", "false", "no", "off":
		return false
	}
	return true
}

// configureLoopDetection 根据本次运行的 env 设置循环检测开关。
// agent 的实现是进程级原子开关，默认开启；这里在每次 Execute 入口都显式设置一次，
// 使开关状态由本次运行的 env 唯一决定，不受上一次运行的残留影响。
//
// 关闭（=0 等假值）时零副作用：不收集事件、不调用检测器，消息/事件/会话记录不变。
func configureLoopDetection(env func(string) string) {
	agent.SetLoopDetection(loopDetectionEnabledFromEnv(env))
}
