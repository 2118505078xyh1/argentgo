package llm

import "math"

const (
	// maxBackoffMs 是可安全转换回 int64 的毫秒上限；
	// 比它更大的浮点值一律饱和，避免 float→int 的实现定义行为。
	maxBackoffMs = int64(^uint64(0) >> 1)
	// backoffHashStep 是混合 seed 与 attempt 的黄金比例常数。
	backoffHashStep = uint64(0x9e3779b97f4a7c15)
)

// BackoffConfig 是 BackoffDelay 的输入参数，按值传递，函数不会修改它。
//
// BaseMs 是首次等待的毫秒数（负数按 0 处理）。
// MaxMs > 0 时作为延迟上限；MaxMs <= 0 表示不设上限，
// 此时溢出只会饱和到 int64 最大值，不会回绕成负数。
// Factor 是每轮尝试的乘数；非正数、NaN 或无穷大按 1 处理（即不增长）。
// Jitter 开启后，延迟会在 [计算值/2, 计算值] 内按 seed+attempt 确定性取值。
type BackoffConfig struct {
	BaseMs int64
	MaxMs  int64
	Factor float64
	Jitter bool
}

// BackoffDelay 返回第 attempt 次尝试前的等待毫秒数，attempt 从 0 开始，
// 负数视为 0。延迟按 BaseMs*Factor^attempt 增长，并钳制到 MaxMs（若设置）。
//
// Jitter 使用基于 seed 与 attempt 的整数混合（splitmix64 finalizer），
// 完全确定性，不读取全局随机数源，也不依赖 math/rand 的全局状态。
//
// 这是纯函数：不修改配置、不产生 NaN、不会因超大 attempt 变慢或溢出。
// math.Pow 对大指数会直接返回 +Inf，随后被饱和到上限。
func BackoffDelay(attempt int, cfg BackoffConfig, seed int64) int64 {
	if attempt < 0 {
		attempt = 0
	}
	factor := cfg.Factor
	if factor <= 0 || math.IsNaN(factor) || math.IsInf(factor, 0) {
		factor = 1
	}
	base := cfg.BaseMs
	if base < 0 {
		base = 0
	}

	hasMax := cfg.MaxMs > 0
	pow := math.Pow(factor, float64(attempt))
	delayF := float64(base) * pow

	var delay int64
	switch {
	case math.IsNaN(delayF) || delayF <= 0:
		return 0
	case math.IsInf(delayF, 1):
		if hasMax {
			return cfg.MaxMs
		}
		return maxBackoffMs
	case hasMax && delayF >= float64(cfg.MaxMs):
		delay = cfg.MaxMs
	case delayF >= float64(maxBackoffMs):
		delay = maxBackoffMs
	default:
		delay = int64(delayF)
	}
	if delay <= 0 || !cfg.Jitter {
		return delay
	}

	// splitmix64 finalizer：纯整数混合，跨平台确定性。
	mixed := backoffMix64(uint64(seed) + uint64(attempt)*backoffHashStep + backoffHashStep)
	ratio := float64(mixed>>11) / float64(uint64(1)<<53) // [0, 1)
	scale := 1 - 0.5*ratio                               // [0.5, 1]
	jittered := float64(delay) * scale
	// 浮点舍入防御：抖动只能缩短延迟，绝不能增大它或越界。
	if jittered >= float64(delay) {
		return delay
	}
	if jittered <= 0 {
		return 0
	}
	d := int64(jittered)
	if d > delay {
		d = delay
	}
	return d
}

// backoffMix64 是 splitmix64 的 finalizer：对输入位做充分雪崩混合，
// 不含任何全局状态，因此同一 (seed, attempt) 永远得到同一结果。
func backoffMix64(x uint64) uint64 {
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	x ^= x >> 31
	return x
}
