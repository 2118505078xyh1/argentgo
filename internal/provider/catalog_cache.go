// Package provider 模型目录刷新策略。
//
// 本文件移植 omp 的 model-manager.ts 的策略部分，不含任何 I/O：无 HTTP 发现、无缓存数据库、无时钟/副作用。
// 调用方拥有实际的抓取和持久化；它调用 ShouldRefresh 决定是否抓取，Merge 把抓取的名册与静态表合并，
// 以及 FingerprintStatic 使针对较旧静态目录写入的缓存失效。保持策略纯函数使其确定且可从任何 goroutine 安全调用。
package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// 默认目录新鲜度旋钮，匹配 omp 的 DEFAULT_CACHE_TTL_MS（2h）和 NON_AUTHORITATIVE_RETRY_MS（5min）。
// 调用方可以传入自己的值。
const (
	DefaultCatalogTTL       = 2 * time.Hour
	DefaultStaleRetryWindow = 5 * time.Minute
)

// RefreshPolicy 控制何时允许运行动态模型发现。它镜像 omp 的 ModelRefreshStrategy。
type RefreshPolicy string

// 支持的刷新策略。
const (
	// RefreshOnline 总是从动态源刷新，即使存在新鲜的权威快照。
	RefreshOnline RefreshPolicy = "online"
	// RefreshOffline 从不刷新；只使用静态数据和已有缓存。
	RefreshOffline RefreshPolicy = "offline"
	// RefreshOnlineIfUncached 在没有快照、快照超过 TTL，或非权威快照超过短暂重试窗口时刷新。这是默认策略。
	RefreshOnlineIfUncached RefreshPolicy = "online-if-uncached"
)

// Model 是策略层所需的最小目录行：用于合并/去重的稳定标识和显示名称，使发现的行的名称能覆盖静态行的名称。
// 完整线上模型在 internal/llm 中；调用方在集成边界在两者之间映射。
type Model struct {
	ID   string
	Name string
}

// CatalogSnapshot 是某 provider 模型目录的时间点视图。Authoritative 表示快照来自完整发现结果
// （而非失败抓取后的回退）；Stale 标记不得享受完整 TTL 的快照。FetchedAt 在从未存储快照时为零。
type CatalogSnapshot struct {
	Models        []Model
	Authoritative bool
	Stale         bool
	FetchedAt     time.Time
}

// ParseRefreshPolicy 把配置/CLI 字符串规范化为 RefreshPolicy。布尔值报告该值是否被识别。
func ParseRefreshPolicy(value string) (RefreshPolicy, bool) {
	policy := RefreshPolicy(strings.ToLower(strings.TrimSpace(value)))
	switch policy {
	case RefreshOnline, RefreshOffline, RefreshOnlineIfUncached:
		return policy, true
	default:
		return "", false
	}
}

// ShouldRefresh 决定是否现在应运行动态发现。策略语义：
//
//   - offline：从不。
//   - online：总是。
//   - online-if-uncached（以及任何未识别的策略，视为保守默认）：在没有快照、快照年龄超过 ttl，
//     或非权威且年龄达到短重试窗口时刷新。
//
// 只有 Authoritative 为 true 且 Stale 为 false 时快照才算权威，因此陈旧快照在短重试窗口刷新，而非等完整 TTL。
// 零 FetchedAt 表示"未缓存"。负年龄（时钟倒退）被限制为零。传入非正的 staleRetryWindow 使非权威快照立即刷新。
func ShouldRefresh(snapshot CatalogSnapshot, now time.Time, ttl, staleRetryWindow time.Duration, policy RefreshPolicy) bool {
	if policy == RefreshOffline {
		return false
	}
	if policy == RefreshOnline {
		return true
	}
	if snapshot.FetchedAt.IsZero() {
		return true
	}

	age := now.Sub(snapshot.FetchedAt)
	if age < 0 {
		age = 0
	}
	if age > ttl {
		return true
	}
	if snapshot.Stale || !snapshot.Authoritative {
		return age >= staleRetryWindow
	}
	return false
}

// Merge 按 ID 把发现的模型叠加到静态目录上。静态行保持顺序；ID 已知的行就地替换，仅发现的行追加。
// 空 ID 被忽略，ID 的最后一个重复项胜出（匹配 omp 中基于 map 的合并）。
// 返回的切片是全新的且绝不与任一输入共享底层数组。
func Merge(static, discovered []Model) []Model {
	order := make([]string, 0, len(static)+len(discovered))
	byID := make(map[string]Model, len(static)+len(discovered))
	put := func(models []Model) {
		for _, model := range models {
			if model.ID == "" {
				continue
			}
			if _, exists := byID[model.ID]; !exists {
				order = append(order, model.ID)
			}
			byID[model.ID] = model
		}
	}
	put(static)
	put(discovered)

	out := make([]Model, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out
}

// FingerprintStatic 返回静态目录的稳定内容标识。当当前指纹不同（静态表升级或策略修改）时，针对某一指纹写入的缓存快照必须丢弃。
// 每个字段加长度前缀避免模型 id 与名称之间的歧义。
func FingerprintStatic(models []Model) string {
	var b strings.Builder
	for _, model := range models {
		b.WriteString(strconv.Itoa(len(model.ID)))
		b.WriteByte(':')
		b.WriteString(model.ID)
		b.WriteString(strconv.Itoa(len(model.Name)))
		b.WriteByte(':')
		b.WriteString(model.Name)
		b.WriteByte('\n')
	}
	sum := sha256.Sum256([]byte(b.String()))
	return "catalog-v1:" + hex.EncodeToString(sum[:])
}
