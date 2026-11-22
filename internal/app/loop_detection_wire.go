package app

import (
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/agent"
)

// loopDetectionEnableEnv 是工具调用循环检测的启用开关。
// 真值与仓库其他 opt-in 一致（1 / true / yes / on，忽略大小写与首尾空白）；
// 未设置或为其他值时保持默认关闭，运行行为与历史会话逐字节一致。
const loopDetectionEnableEnv = "ARGENT_LOOP_DETECTION"

// loopDetectionEnabledFromEnv 报告本次运行的开关是否为真值。
func loopDetectionEnabledFromEnv(env func(string) string) bool {
	if env == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(env(loopDetectionEnableEnv))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// configureLoopDetection 根据本次运行的 env 设置循环检测开关。
// agent 的实现是进程级原子开关，默认关闭；此前只有测试调用 SetLoopDetection，
// 生产路径永远无法开启。这里在每次 Execute 入口都显式设置一次，使开关状态
// 由本次运行的 env 唯一决定，不受上一次运行的残留影响。
//
// 关闭（默认）时零副作用：不收集事件、不调用检测器，消息/事件/会话记录不变。
func configureLoopDetection(env func(string) string) {
	agent.SetLoopDetection(loopDetectionEnabledFromEnv(env))
}
