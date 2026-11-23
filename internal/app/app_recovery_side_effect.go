package app

import "strings"

// 本文件承载 TODO #20：中断/降级恢复公告后追加一段有界副作用告示。
// 被中断的工具可能已部分执行，后台 exec/jobs 会话可能仍在运行；模型在重复写操作
// 或重启服务前应先核对状态。默认开启，ARGENT_INTERRUPTED_SIDE_EFFECT_NOTICE=0
// 仅作紧急回退；它只加信息，绝不触碰 NeedsConfirmation（安全语义仍由分类唯一决定）。

const interruptedSideEffectEnv = "ARGENT_INTERRUPTED_SIDE_EFFECT_NOTICE"

// 单一固定短句，长度有界且不含任何用户/记录原文，避免把转录内容带回上下文。
const interruptedSideEffectNotice = " (注意：被中断的工具可能已部分执行，后台 exec 会话可能仍在运行；重复写操作或启动服务前请先核对状态)"

// interruptedSideEffectMarker 用于幂等保护：同一公告不会被追加两次后缀。
const interruptedSideEffectMarker = "被中断的工具可能已部分执行"

func interruptedSideEffectEnabled(env func(string) string) bool {
	if env == nil {
		return true
	}
	return strings.TrimSpace(env(interruptedSideEffectEnv)) != "0"
}

// withInterruptedSideEffectNotice 在已有的 interrupted/degraded 恢复公告末尾追加告示。
// 干净会话（Notice 为空）完全不进入，绝不产生凭空公告；=0 回退时逐字节不变。
func withInterruptedSideEffectNotice(decision *recoveryDecision, env func(string) string) {
	if decision == nil || decision.Notice == "" || !interruptedSideEffectEnabled(env) {
		return
	}
	if strings.Contains(decision.Notice, interruptedSideEffectMarker) {
		return
	}
	decision.Notice += interruptedSideEffectNotice
}
