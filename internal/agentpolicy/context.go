package agentpolicy

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
	"sort"
)

// SourceID 标识一段上下文的来源。取值采用"类别.子类"的稳定字符串，
// 便于日志、缓存键与跨进程比较；调用方不应把它当作任意用户输入。
type SourceID string

const (
	SourceSystemStatic      SourceID = "system.static"
	SourceSystemDynamic     SourceID = "system.dynamic"
	SourceToolsSpecs        SourceID = "tools.specs"
	SourceHistoryUser       SourceID = "history.user"
	SourceHistoryTool       SourceID = "history.tool"
	SourceSteer             SourceID = "steer"
	SourceReminder          SourceID = "reminder"
	SourceCompactionSummary SourceID = "compaction.summary"
)

// Source 是上下文状态中的一个有序段。
//   - ID 标识来源；
//   - Stable 标记该段是否参与 Epoch 与 Diff；易变段（steer/reminder 等）应为 false；
//   - Digest 是该段内容的指纹，由调用方计算，本包只读取、不解释其格式。
type Source struct {
	ID     SourceID
	Stable bool
	Digest string
}

// ContextState 是一段有序的来源序列，零值表示空状态。
// 它只保存来源切片，不含共享指针：构造与派生都复制切片，保证纯函数语义。
type ContextState struct {
	sources []Source
}

// NewContextState 由来源切片构造状态。它复制入参，调用方随后修改 srcs 不会影响结果。
func NewContextState(srcs []Source) ContextState {
	return ContextState{sources: append([]Source(nil), srcs...)}
}

// Sources 按原顺序返回来源的副本，供调用方读取或序列化。
func (s ContextState) Sources() []Source {
	return append([]Source(nil), s.sources...)
}

// Len 返回状态中的来源数量。
func (s ContextState) Len() int { return len(s.sources) }

// stableSources 返回 Stable 来源的副本，保持原顺序。volatile 来源被完全忽略。
func stableSources(s ContextState) []Source {
	out := make([]Source, 0, len(s.sources))
	for _, src := range s.sources {
		if src.Stable {
			out = append(out, src)
		}
	}
	return out
}

// Epoch 返回状态的"稳定纪元"指纹：只取 Stable 来源，按 (ID, Digest) 排序后，
// 逐字段写入 SHA-256，输出 64 位小写十六进制。
//
// 设计要点：
//   - 排序使 Epoch 与 Stable 来源的插入顺序无关，仅由内容集合决定——重排稳定段
//     不算缓存失效，符合"固定顺序"的约定；
//   - 长度前缀按字节计，故 CJK 多字节字符也正确；它消除 "ab"+"c" 与 "a"+"bc"
//     这类拼接歧义，杜绝内容碰撞；
//   - volatile 来源完全不参与，因此 steer/reminder 等易变段的变化不会改变 Epoch；
//   - 空状态返回空输入的 SHA-256，是确定性的合法纪元。
//
// 纯函数：不修改接收者。
func (s ContextState) Epoch() string {
	items := stableSources(s)
	sort.Slice(items, func(i, j int) bool {
		if items[i].ID != items[j].ID {
			return items[i].ID < items[j].ID
		}
		return items[i].Digest < items[j].Digest
	})
	h := sha256.New()
	for _, src := range items {
		writeField(h, string(src.ID))
		writeField(h, src.Digest)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// writeField 以"8 字节大端字节长度 + 原始字节"的形式写入哈希，
// 保证任意内容（含 CJK、NUL、超长串）的编码无歧义。
func writeField(h hash.Hash, field string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(field)))
	_, _ = h.Write(length[:])
	_, _ = h.Write([]byte(field))
}

// DeltaKind 是单个来源差异的类别。
type DeltaKind string

const (
	DeltaAdded   DeltaKind = "added"
	DeltaRemoved DeltaKind = "removed"
	DeltaChanged DeltaKind = "changed"
)

// SourceDelta 描述一个 Stable 来源在两个状态间的差异。
// Before/After 分别是 prev/next 中的 Digest；Added 时 Before 为空，Removed 时 After 为空。
type SourceDelta struct {
	ID     SourceID
	Kind   DeltaKind
	Before string
	After  string
}

// Diff 比较 prev 与 next 的 Stable 来源，按遍历顺序返回差异序列。
// volatile 来源被完全忽略；两者稳定段完全相同（含顺序与 Digest）时返回 nil。
//
// 对齐策略：对 ID 序列求最长公共子序列（LCS）。
//   - 匹配上的 ID：Digest 相同则无差异，不同则记为 DeltaChanged；
//   - 仅存在于 prev 的项记为 DeltaRemoved；
//   - 仅存在于 next 的项记为 DeltaAdded。
//
// 因此重排稳定段会被报告为 removal+addition（顺序敏感），而仅在原地改动 Digest
// 会被报告为一次 Changed。重复 ID 由 LCS 自然处理。
// 纯函数：不修改入参。
func Diff(prev, next ContextState) []SourceDelta {
	p := stableSources(prev)
	n := stableSources(next)
	m, k := len(p), len(n)

	// dp[i][j] = p[i:] 与 n[j:] 按 ID 的 LCS 长度。
	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, k+1)
	}
	for i := m - 1; i >= 0; i-- {
		for j := k - 1; j >= 0; j-- {
			switch {
			case p[i].ID == n[j].ID:
				dp[i][j] = dp[i+1][j+1] + 1
			case dp[i+1][j] >= dp[i][j+1]:
				dp[i][j] = dp[i+1][j]
			default:
				dp[i][j] = dp[i][j+1]
			}
		}
	}

	var deltas []SourceDelta
	i, j := 0, 0
	for i < m || j < k {
		switch {
		case i < m && j < k && p[i].ID == n[j].ID:
			if p[i].Digest != n[j].Digest {
				deltas = append(deltas, SourceDelta{
					ID: p[i].ID, Kind: DeltaChanged, Before: p[i].Digest, After: n[j].Digest,
				})
			}
			i++
			j++
		case i < m && (j == k || dp[i+1][j] >= dp[i][j+1]):
			deltas = append(deltas, SourceDelta{ID: p[i].ID, Kind: DeltaRemoved, Before: p[i].Digest})
			i++
		default:
			deltas = append(deltas, SourceDelta{ID: n[j].ID, Kind: DeltaAdded, After: n[j].Digest})
			j++
		}
	}
	return deltas
}

// Boundary 是接纳新来源的边界状态。
type Boundary string

const (
	BoundaryNone   Boundary = ""       // 零值：未设置，按 no-op 处理
	BoundaryClosed Boundary = "closed" // 显式关闭，no-op
	BoundaryOpen   Boundary = "open"   // 开放：允许接纳
)

// AdmitAtBoundary 在 boundary 处把 src 接纳进 prev，返回派生后的新状态。
//
// 纯函数、零副作用：
//   - boundary 为零值（BoundaryNone）或 BoundaryClosed 时原样返回 prev（no-op）；
//   - BoundaryOpen 且 src.ID 非空时，把 src 追加为新的末段；
//   - src 的 ID 为空（零值来源）同样视为 no-op。
//
// 返回的状态与 prev 不共享底层切片，调用方可安全地继续修改。
func AdmitAtBoundary(prev ContextState, boundary Boundary, src Source) ContextState {
	if boundary != BoundaryOpen || src.ID == "" {
		return prev
	}
	out := append([]Source(nil), prev.sources...)
	out = append(out, src)
	return ContextState{sources: out}
}
