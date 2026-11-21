package hooks

import (
	"fmt"
	"sync"
)

// SnapshotStore 是钩子配置快照的线程安全持有者，也是热重载的"接线"原语。
//
// 它不含 I/O、定时器或 goroutine：调用方轮询到新内容后用 Swap 采纳，读/解析/
// 二次校验失败时用 NoteFailure 记账并保留 last-good。零值即可用：未采纳前
// Load/LastGood 返回零值快照，代数为 0。
//
// "当前生效"与"最近成功"在这里是同一个快照——失败从不改写它（见 NoteFailure），
// 因此调用方在失败分支里读到的必然是上一次成功采纳的快照，即失败回退 last-good。
//
// gen 是独立于 ConfigSnapshot.Generation 的单调代数：仅当 gen 严格大于当前代数
// 时 Swap 才生效，防止迟到的旧换代覆盖已生效的新快照。快照自身的 Generation 与
// Fingerprint 按传入值原样保存，Swap 不改写它们。
type SnapshotStore struct {
	mu      sync.RWMutex
	snap    ConfigSnapshot // 当前生效且最近一次成功采纳的快照（失败时保留）
	gen     uint64         // 当前代数（单调）
	lastErr error          // 最近一次失败，由 NoteFailure 写入
}

// Load 返回当前生效的快照。零值存储返回零值快照。
func (s *SnapshotStore) Load() ConfigSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snap
}

// LastGood 返回最近一次成功采纳的快照。
//
// 它与 Load 返回同一份数据：NoteFailure 只记账、不回退快照，所以"当前生效"始终
// 是一个 last-good。两个名字分别服务于调用点的两种意图（取用 / 兜底上报），
// 而不是两份可能失同步的状态。
func (s *SnapshotStore) LastGood() ConfigSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snap
}

// Swap 在 gen 严格大于当前代数时把 next 采纳为当前快照（并同步 last-good），
// 返回 true；gen 小于或等于当前代数时什么也不做并返回 false。
//
// next 按原样保存：不读取其来源、不改写其 Generation/Fingerprint，也不做信任
// 判定——信任门属于 SnapshotFromSources/LoadSource 与 ValidateBeforeAdopt。
// 成功的 Swap 不清除 lastErr；它记录的是"最近一次失败事件"，用于诊断而非状态机。
func (s *SnapshotStore) Swap(next ConfigSnapshot, gen uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if gen <= s.gen {
		return false
	}
	s.snap = next
	s.gen = gen
	return true
}

// NoteFailure 记录最近一次失败，供诊断使用；它不改动快照与代数。
//
// 因此加载或校验失败时，调用方无需额外状态即可用 Load/LastGood 读回上一次成功
// 采纳的快照。错误按原值保存，调用方可用 errors.Is 匹配哨兵错误。
func (s *SnapshotStore) NoteFailure(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastErr = err
}

// LastError 返回 NoteFailure 记录的最后错误；从未失败过时返回 nil。
func (s *SnapshotStore) LastError() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastErr
}

// validateTrustEvidence 对一组逐源信任凭据（信任键 + 内容摘要）做纵深复核：
// 任一条目未授信即返回包装 ErrUntrusted 的错误；空凭据视为无需核验。
//
// 它是 ValidateBeforeAdopt 在"源信息可得"时的执行体，单独抽出便于直接覆盖
// 未授信路径。核验只查表，不读取来源、不自动授信，也不回灌主信任存储。
func validateTrustEvidence(entries []TrustEntry, trust *TrustStore) error {
	for _, entry := range entries {
		if trust == nil || !trust.IsTrusted(entry.Path, entry.Digest) {
			return fmt.Errorf("hooks: %s: %w", entry.Path, ErrUntrusted)
		}
	}
	return nil
}

// ValidateBeforeAdopt 是在采纳快照之前的纵深防御：若快照能给出与其 Config 关联的
// 逐源信任键/摘要，则逐条复核它们仍在 trust 内；任一条目未授信返回 ErrUntrusted。
//
// 当前 ConfigSnapshot 只携带合并后的 Config、聚合 Fingerprint 与 Generation，
// 不含逐源信息——聚合指纹不可逆，Config 也不保留各来源的 Document。因此源信息
// 不可得，函数返回 nil 并直接进入空凭据分支：信任已在 SnapshotFromSources →
// LoadSource 阶段校验过（先核验摘要、后解析），这里既不重复读来源、不自动授信，
// 也不把任何内容回灌到主信任存储。待快照承载逐源条目后，把空凭据换成证据列表
// 即可复用 validateTrustEvidence，无需改动调用方。
func ValidateBeforeAdopt(snap ConfigSnapshot, trust *TrustStore) error {
	if snap.Config == nil {
		return nil
	}
	return validateTrustEvidence(nil, trust)
}
