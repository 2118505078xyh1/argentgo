package jobs

import (
	"sort"
	"time"
	"unicode/utf8"
)

// previewMarker 在预览被截断时追加，表明后面还有未展示的内容。
// 使用纯 ASCII 便于按字节精确计算截断位置（一个字节一个字符）。
const previewMarker = "...[truncated]"

// RetentionConfig 是后台作业的有界保留策略。零值表示全部关闭，此时
// 注册表的读写、淘汰与日志读取行为与未引入本特性时逐字节一致。
//
// 这是 opt-in 特性：只有显式调用 SetRetention 配置非零字段后，
// TTL 过期、LRU 容量淘汰与有界预览才会生效。
type RetentionConfig struct {
	// TTL 大于零时，启动时间早于 now-TTL 的终态作业被视为过期并被淘汰。
	// 运行中的作业永不过期，避免误杀仍存活的子进程。
	TTL time.Duration
	// MaxEntries 大于零时，注册表最多保留这么多条目；超出时按最近使用
	// 时间（LRU）从旧到新淘汰，并优先淘汰终态作业。
	MaxEntries int
	// PreviewBytes 大于零时，Preview 把结果截断到该字节数以内，
	// 并在截断时追加 previewMarker。
	PreviewBytes int
}

// enabled 报告是否开启了任何有界保留特性。
func (c RetentionConfig) enabled() bool {
	return c.TTL > 0 || c.MaxEntries > 0 || c.PreviewBytes > 0
}

// lruEntry 是 LRU 淘汰决策所需的最小视图。
type lruEntry struct {
	id       string
	lastUsed time.Time
	terminal bool
}

// expiredByTTL 挑选在 now 时刻已存活超过 ttl 的终态作业 ID。
//
// ttl<=0 时返回 nil；只考虑终态作业（运行中作业永不过期）。比较使用
// now.Sub(StartedAt) 而非 now.Add(-ttl)，避免超大 ttl 使时间回绕。
// 纯函数，不修改入参，结果按 ID 字节序排序以保证确定性。
func expiredByTTL(jobs []JobInfo, now time.Time, ttl time.Duration) []string {
	if ttl <= 0 {
		return nil
	}
	var ids []string
	for _, job := range jobs {
		if job.Status == StatusRunning {
			continue
		}
		if now.Sub(job.StartedAt) >= ttl {
			ids = append(ids, job.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

// selectLRUEvictionCandidates 在条目数超过 max 时挑选要淘汰的 ID。
//
// 排序规则（确定性，与输入顺序无关）：
//   - 优先终态作业（terminal），其次才考虑仍在运行的作业；
//   - 同一类内按最近使用时间从旧到新（真正的 LRU，而非仅启动时间）；
//   - 时间相同则按 ID 字节序排列，保证结果稳定且对 CJK 名称安全。
//
// 返回的 ID 数量恰好等于超出上限的数量（被 len(entries) 截断）。
// max<=0（或 entries 为空）时返回 nil。纯函数，不修改入参。
func selectLRUEvictionCandidates(entries []lruEntry, max int) []string {
	over := len(entries) - max
	if over <= 0 {
		return nil
	}
	if over > len(entries) {
		over = len(entries)
	}

	candidates := make([]lruEntry, len(entries))
	copy(candidates, entries)
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.terminal != b.terminal {
			return a.terminal // 终态优先被淘汰
		}
		if !a.lastUsed.Equal(b.lastUsed) {
			return a.lastUsed.Before(b.lastUsed)
		}
		return a.id < b.id
	})

	ids := make([]string, over)
	for i := range ids {
		ids[i] = candidates[i].id
	}
	return ids
}

// truncateRunes 把 s 截断到不超过 limit 字节，且绝不切断 UTF-8 字符。
func truncateRunes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(s) <= limit {
		return s
	}
	end := limit
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end]
}

// boundPreview 把 text 截断到至多 maxBytes 字节，并在截断时追加
// previewMarker。maxBytes<=0 表示不限制，原样返回。结果始终是不超过
// maxBytes 的合法 UTF-8：截断按 rune 边界回退，标记本身也是纯 ASCII。
func boundPreview(text string, maxBytes int) string {
	if maxBytes <= 0 || len(text) <= maxBytes {
		return text
	}
	if maxBytes <= len(previewMarker) {
		// 连标记都放不下：只做 rune 安全的纯截断，不再追加标记。
		return truncateRunes(text, maxBytes)
	}
	head := truncateRunes(text, maxBytes-len(previewMarker))
	return head + previewMarker
}

// SetRetention 配置有界保留策略。传入零值 RetentionConfig{} 会关闭全部
// 特性并恢复默认（逐字节不变）行为。可在运行时重复调用。
//
// 开启 MaxEntries 时会为现有条目补齐访问时间戳（以当前时间为准），
// 使 LRU 账本从配置生效的那一刻起可用。
func (r *Registry) SetRetention(cfg RetentionConfig) {
	r.mu.Lock()
	r.retention = cfg
	if cfg.MaxEntries > 0 {
		if r.lastUsed == nil {
			r.lastUsed = make(map[string]time.Time, len(r.jobs))
		}
		now := time.Now().UTC()
		for id := range r.jobs {
			if _, ok := r.lastUsed[id]; !ok {
				r.lastUsed[id] = now
			}
		}
	}
	r.mu.Unlock()
}

// noteAccessLocked 在启用 LRU 时记录一次单条目访问。调用方必须持有 mu。
// 它只更新内存账本，不改变版本号或脏标记，因此绝不影响持久化字节。
func (r *Registry) noteAccessLocked(id string) {
	if r.retention.MaxEntries <= 0 {
		return
	}
	if r.lastUsed == nil {
		r.lastUsed = make(map[string]time.Time)
	}
	r.lastUsed[id] = time.Now().UTC()
}

// noteAccessAllLocked 在启用 LRU 时记录一次全量访问（List）。调用方必须持有 mu。
func (r *Registry) noteAccessAllLocked() {
	if r.retention.MaxEntries <= 0 {
		return
	}
	if r.lastUsed == nil {
		r.lastUsed = make(map[string]time.Time, len(r.jobs))
	}
	now := time.Now().UTC()
	for id := range r.jobs {
		r.lastUsed[id] = now
	}
}

// forgetAccessLocked 清理单条目的 LRU 账本。调用方必须持有 mu。
func (r *Registry) forgetAccessLocked(id string) {
	if r.lastUsed != nil {
		delete(r.lastUsed, id)
	}
}

// EnforceTTL 淘汰启动时间超过 TTL 的终态作业，返回被淘汰的 ID。
// 未开启 TTL（TTL<=0）时返回 nil 且不改变任何作业。
//
// 淘汰复用 Remove：它负责持久化并删除日志。单个淘汰失败被忽略，
// 以免影响其余作业。
func (r *Registry) EnforceTTL() []string {
	r.mu.Lock()
	ttl := r.retention.TTL
	if ttl <= 0 {
		r.mu.Unlock()
		return nil
	}
	infos := make([]JobInfo, 0, len(r.jobs))
	for _, job := range r.jobs {
		infos = append(infos, *job)
	}
	ids := expiredByTTL(infos, time.Now().UTC(), ttl)
	for _, id := range ids {
		r.forgetAccessLocked(id)
	}
	r.mu.Unlock()

	for _, id := range ids {
		_ = r.Remove(id)
	}
	return ids
}

// EnforceLRU 在超过 MaxEntries 时按 LRU 淘汰多余作业，返回被淘汰的 ID。
// 未开启上限（MaxEntries<=0）或未超限时返回 nil 且不改变任何作业。
func (r *Registry) EnforceLRU() []string {
	r.mu.Lock()
	max := r.retention.MaxEntries
	if max <= 0 {
		r.mu.Unlock()
		return nil
	}
	entries := make([]lruEntry, 0, len(r.jobs))
	for id, job := range r.jobs {
		used, ok := r.lastUsed[id]
		if !ok {
			// 账本缺条目时回退到启动时间，并补齐账本。
			used = job.StartedAt
			if r.lastUsed == nil {
				r.lastUsed = make(map[string]time.Time)
			}
			r.lastUsed[id] = used
		}
		entries = append(entries, lruEntry{
			id:       id,
			lastUsed: used,
			terminal: job.Status != StatusRunning,
		})
	}
	ids := selectLRUEvictionCandidates(entries, max)
	for _, id := range ids {
		r.forgetAccessLocked(id)
	}
	r.mu.Unlock()

	for _, id := range ids {
		_ = r.Remove(id)
	}
	return ids
}

// EnforceRetention 依次执行 TTL 过期淘汰与 LRU 容量淘汰，
// 返回被淘汰 ID 的并集（去重）。未开启任何策略时返回 nil。
func (r *Registry) EnforceRetention() []string {
	expired := r.EnforceTTL()
	evicted := r.EnforceLRU()
	if len(expired) == 0 {
		return evicted
	}
	if len(evicted) == 0 {
		return expired
	}

	seen := make(map[string]struct{}, len(expired)+len(evicted))
	out := make([]string, 0, len(expired)+len(evicted))
	for _, id := range append(append([]string{}, expired...), evicted...) {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// Preview 返回作业日志的有界预览。开启 PreviewBytes 时结果被截断到该
// 字节数以内（必要时追加省略标记）；未开启时行为与 Tail 完全一致，
// 这是默认路径，因此既有调用方逐字节不受影响。
func (r *Registry) Preview(id string, n int) (string, error) {
	text, err := r.Tail(id, n)
	if err != nil {
		return "", err
	}
	r.mu.Lock()
	limit := r.retention.PreviewBytes
	r.mu.Unlock()
	return boundPreview(text, limit), nil
}
