package app

import "time"

// 缓存未命中的归因标签。它们是不透明的 ASCII 标识符，便于日志/指标聚合，
// 而非面向用户的文案；模型名即使含 CJK 也不会进入这些标签，
// 因此返回值始终是稳定的固定字节序列。
const (
	cacheMissModelSwitched = "model_switched"
	cacheMissIdleExpired   = "idle_expired"
)

// cacheMissReason 归因一次提示缓存未命中：优先判断是否切换了模型，
// 其次判断是否因空闲超过 TTL 而过期；两者都不是时返回空串（表示无需归因）。
//
// 它是纯函数：只依赖四个入参，不读时钟、不访问全局状态、不产生副作用，
// 因此对相同输入总是返回相同结果。判定顺序是确定的——模型名不同总是优先于
// 空闲超时，即使二者同时成立：切换模型是更根本、更可操作的未命中原因。
//
// 入参约定：
//   - prevModel / curModel 为上一次与本次使用的模型名，按字节精确比较（==），
//     不做裁剪或规范化；CJK 模型名按完整 UTF-8 序列比较，不会因字节截断产生半个汉字或误判。
//   - idle 为距上次缓存活动的空闲时长，ttl 为缓存条目的存活时长；
//     只有当 idle 严格大于 ttl（即"超 TTL"）时才判为空闲过期。
//   - 返回值只会是 cacheMissModelSwitched、cacheMissIdleExpired 或空串之一。
func cacheMissReason(prevModel, curModel string, idle time.Duration, ttl time.Duration) string {
	if prevModel != curModel {
		return cacheMissModelSwitched
	}
	if idle > ttl {
		return cacheMissIdleExpired
	}
	return ""
}
