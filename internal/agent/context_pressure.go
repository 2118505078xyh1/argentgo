package agent

import (
	"math/bits"
)

// 本文件提供"上下文压力等级"纯原语：把已用 token 与预算的比例映射到五个离散等级。
// 它是纯函数、默认不接线——只有显式调用 ClassifyPressure / PressureLabel 时才会被用到，
// 不改动任何现有类型、事件或循环，也不引入新的包间依赖。
//
// 设计动机（R41）：R28 已有 context_pressure 事件，但缺少一个"压力等级"纯函数，
// 供 UI、日志与后续触发决策复用。本原语补上这一缺口：只消费两个整数，输出稳定的
// 等级值与中英双语标签；相同输入总产生相同输出，不修改任何入参。

// PressureLevel 是上下文压力等级。数值从小到大表示压力递增，便于比较与排序。
type PressureLevel int

// 五个等级。iota 保证取值连续且稳定，便于快照与日志比对。
const (
	levelLow      PressureLevel = iota // 低于 50%：压力低
	levelNormal                        // 50%~70%：正常
	levelElevated                      // 70%~85%：升高
	levelHigh                          // 85%~95%：高
	levelCritical                      // 95% 及以上，或已用超过预算：危急
)

// 分级阈值，用分子/分母常量表示精确比例，避免浮点边界歧义。
// 判定规则（上界均不含）：<1/2 为 Low，<7/10 为 Normal，<17/20 为 Elevated，
// <19/20 为 High，其余（含 ≥19/20 与超预算）为 Critical。
const (
	pressureLowNum      = 1
	pressureLowDen      = 2
	pressureNormalNum   = 7
	pressureNormalDen   = 10
	pressureElevatedNum = 17
	pressureElevatedDen = 20
	pressureHighNum     = 19
	pressureHighDen     = 20
)

// ClassifyPressure 按 used/budget 比例把上下文用量映射到压力等级。
//
// 它是纯函数：只读两个整数参数，不读取全局状态、不产生 I/O、不修改任何入参
// （入参是值类型），相同输入总产生相同结果。
//
//   - budget <= 0 表示"不设限"，无论 used 为何都返回 levelLow；
//   - used <= 0 表示没有用量，同样返回 levelLow；
//   - 其余情况按 used/budget 与四个阈值做整数交叉相乘比较，严格小于某阈值
//     落入该阈值之前的档位，等于阈值则进入更高一档（即上界不含）。
//
// 比例比较一律走 fracLess 做 128 位交叉相乘，绝不先算浮点商，也绝不让
// used*分母 或 阈值分子*budget 在 int 中溢出；因此即使 used、budget 接近
// math.MaxInt，边界仍然精确。used 超过 budget（比例 > 1）必然落入 Critical。
func ClassifyPressure(used, budget int) PressureLevel {
	if budget <= 0 || used <= 0 {
		return levelLow
	}
	switch {
	case fracLess(used, budget, pressureLowNum, pressureLowDen):
		return levelLow
	case fracLess(used, budget, pressureNormalNum, pressureNormalDen):
		return levelNormal
	case fracLess(used, budget, pressureElevatedNum, pressureElevatedDen):
		return levelElevated
	case fracLess(used, budget, pressureHighNum, pressureHighDen):
		return levelHigh
	default:
		return levelCritical
	}
}

// fracLess 报告 a/b < c/d 是否成立（要求 a、c 非负且 b、d 为正）。
// 它用 math/bits.Mul64 把 a*d 与 c*b 算成 128 位乘积再比较，从而避免 int 乘法溢出；
// 仅比较大小、不求商，因此对任意 int 入参都精确。
func fracLess(a, b, c, d int) bool {
	ah, al := bits.Mul64(uint64(a), uint64(d))
	ch, cl := bits.Mul64(uint64(c), uint64(b))
	if ah != ch {
		return ah < ch
	}
	return al < cl
}

// PressureLabel 返回等级的稳定文案，形如 "低 (low)"：中文标签在前，括号内是英文键。
// 未知等级返回 "未知 (unknown)"，保证日志与 UI 永不丢失信息。字符串固定，
// 可直接用于快照与比对。
func PressureLabel(level PressureLevel) string {
	switch level {
	case levelLow:
		return "低 (low)"
	case levelNormal:
		return "正常 (normal)"
	case levelElevated:
		return "升高 (elevated)"
	case levelHigh:
		return "高 (high)"
	case levelCritical:
		return "危急 (critical)"
	default:
		return "未知 (unknown)"
	}
}
