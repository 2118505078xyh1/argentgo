package app

import (
	"strings"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// cacheWarmerEnabled 报告提示缓存保活的选择性启用状态。默认关闭：只有 ARGENT_CACHE_WARM=1 才启用它。
// 目前没有任何生产路径接入保活重放，因此启用它只是为未来的调用方武装时间戳记录；
// 它从不会改变线上请求。
func cacheWarmerEnabled(env func(string) string) bool {
	return env != nil && strings.TrimSpace(env("ARGENT_CACHE_WARM")) == "1"
}

// newCacheWarmer 构建一个缓存预热器。调用方传入显式的选择启用决定；
// 默认构造路径使用 cacheWarmerEnabled 的默认值（false），使预热器保持禁用且每个请求逐字节不变。
func newCacheWarmer(enabled bool) *llm.CacheWarmer {
	return &llm.CacheWarmer{Enabled: enabled}
}

// cacheWarmerProbe 把长生命周期引擎会在其边界调用的两个记录钩子归组：
// 在请求发送点调用 RecordRequest，在轮次边界调用 RecordIdle。
// 它是一个薄而惰性的适配器：从不发送 1-token 重放，也从不启动定时器，且预热器禁用时为空操作。
// 目前它没有安装在任何生产路径上（见接线报告）；它的存在是为了让未来的调用方
// 能在一个地方采用这两个钩子，并在不接触网络的情况下测试它们。
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
