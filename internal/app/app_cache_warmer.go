package app

import (
	"strings"
	"time"

	"example.com/argentgo/internal/llm"
)

// cacheWarmerEnabled 报告提示缓存保活的启用状态：默认开启，ARGENT_CACHE_WARM=0
// （允许首尾空白）时紧急关闭。生产接线在 cache_warmer_wire.go：长生命周期 runner
// 持有调度器与内存请求副本，在轮次边界按需发 1-token 重放。
func cacheWarmerEnabled(env func(string) string) bool {
	return env == nil || strings.TrimSpace(env("ARGENT_CACHE_WARM")) != "0"
}

// newCacheWarmer 构建一个缓存预热器。调用方传入显式的启用决定；
// 默认构造路径使用 cacheWarmerEnabled 的默认值（true）。
func newCacheWarmer(enabled bool) *llm.CacheWarmer {
	return &llm.CacheWarmer{Enabled: enabled}
}

// cacheWarmerProbe 把长生命周期引擎会在其边界调用的两个记录钩子归组：
// 在请求发送点调用 RecordRequest，在轮次边界调用 RecordIdle。
// 它是一个薄而惰性的适配器：从不发送 1-token 重放，也从不启动定时器，且预热器禁用时为空操作。
// 生产路径改用 cache_warmer_wire.go 的 warmRecordChatter：它同样不启动定时器，
// 但在真实 Chat 前刷新时钟，并由 runner 在轮次边界执行重放。本探针保留给需要
// 手工驱动两个钩子、且不接触网络的测试。
type cacheWarmerProbe struct {
	warmer *llm.CacheWarmer
}

func (p *cacheWarmerProbe) recordRequest(now time.Time) {
	if p != nil && p.warmer != nil {
		p.warmer.RecordRequest(now)
	}
}

func (p *cacheWarmerProbe) recordIdle(now time.Time) {
	if p != nil && p.warmer != nil {
		p.warmer.RecordIdle(now)
	}
}

// shouldWarm 对应 llm.CacheWarmer.ShouldWarm，但从不对答案采取行动。
// 安装该探针本身无法发出保活请求。
func (p *cacheWarmerProbe) shouldWarm(now time.Time) bool {
	if p == nil || p.warmer == nil {
		return false
	}
	return p.warmer.ShouldWarm(now)
}
