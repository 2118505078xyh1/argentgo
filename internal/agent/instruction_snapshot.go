package agent

// 本文件实现了一种分层指令快照，父会话可以把它传递给子 agent。
// 这一思路借鉴 Codex 的"分层指令快照"：app/memory 层一次性解析用户、会话线程和项目的指令，
// 将它们冻结为一个确定且有界的值，子 agent 继承该值，而不是重新加载或重新解析。
//
// 这里刻意保持 agent 局部：各层以 []string 形式传入，本文件从不导入 internal/memory
// （也不做任何 I/O）。接线层（internal/app，它负责 memory.Load + memory.Block）
// 负责把指令文件转换为三个 []string 层，并传给 BuildSnapshot。
// 把该类型保持为普通值，使其能廉价地复制进子 agent 的 SubagentConfig，
// 并通过 Fingerprint 用作缓存键。

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf8"
)

// MaxSnapshotBytes 是快照所有三层共享的总字节预算。它与 memory 的 32 KiB 合计预算一致，
// 因此继承快照不会使子 agent 的系统提示超过父级已经付出的大小。
const MaxSnapshotBytes = 32 * 1024

// InheritanceNote 由 Inherit 追加到 Thread 层，使子 agent 能看到它的指令
// 是从父会话传递下来的，而非本地解析的。它是一个固定的纯文本标记（无 I/O、无路径）。
const InheritanceNote = "inherited from parent session"

// InstructionSnapshot 是会话已解析指令的有界快照，约定为不可变。
// 各层保存纯文本条目；Bytes 是所有层中存储的总字节数，始终由下面的构造函数重新计算。
//
// 调用方应把这些切片视为只读。Clone（以及内部会克隆的 Inherit / Merge）
// 会在任何修改之前生成独立的副本。
type InstructionSnapshot struct {
	User    []string
	Project []string
	Thread  []string
	Bytes   int
}

// All 按解析顺序返回所有条目：先是 user，再是 thread，最后是 project。
// 返回的切片是新分配的，因此调用方无法通过它修改快照。
func (s InstructionSnapshot) All() []string {
	out := make([]string, 0, len(s.User)+len(s.Thread)+len(s.Project))
	out = append(out, s.User...)
	out = append(out, s.Thread...)
	out = append(out, s.Project...)
	return out
}

// Clone 返回深拷贝：每一层都是新切片，因此修改副本（包括其条目）绝不会改变源对象。
func (s InstructionSnapshot) Clone() InstructionSnapshot {
	return InstructionSnapshot{
		User:    cloneStrings(s.User),
		Project: cloneStrings(s.Project),
		Thread:  cloneStrings(s.Thread),
		Bytes:   s.Bytes,
	}
}

// Fingerprint 返回快照内容的稳定 SHA-256 十六进制摘要。
// 其中混入了层名和每个条目的字节长度，因此把字符串在层间移动（或改变其边界）都会改变键。
// 它适合作为已校验/已应用指令快照的缓存键。
func (s InstructionSnapshot) Fingerprint() string {
	h := sha256.New()
	layers := [...]struct {
		name string
		vals []string
	}{
		{"user", s.User},
		{"thread", s.Thread},
		{"project", s.Project},
	}
	for _, layer := range layers {
		for _, v := range layer.vals {
			// 用 NUL 结尾符做长度前缀是明确且确定的，无需分配拼接的中间字符串。
			fmt.Fprintf(h, "%s:%d:%s\x00", layer.name, len(v), v)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// BuildSnapshot 对三个已解析层做清洗、限界并冻结为快照。
// 总字节预算（MaxSnapshotBytes）在各层间共享；条目按 All() 顺序（user、thread、project）保留，
// 若会越界则在 UTF-8 rune 边界处裁剪。这是纯函数：不访问文件、环境或时钟。
func BuildSnapshot(user, project, thread []string) InstructionSnapshot {
	return normalizeBudget(user, project, thread, MaxSnapshotBytes)
}

// Inherit 返回子 agent 应接收的快照：父级指令的深拷贝，并在 Thread 层追加一个有界的继承标记。
// 标记的字节会先被预留，因此即使父快照已达到大小上限它也始终存在；
// 必要时裁剪/丢弃较旧的 thread 条目来腾出空间，而不是丢失继承信号。
func (s InstructionSnapshot) Inherit() InstructionSnapshot {
	note := scrubEntry(InheritanceNote)
	budget := MaxSnapshotBytes - len(note)
	if budget < 0 {
		budget = 0
	}
	child := normalizeBudget(s.User, s.Project, s.Thread, budget)
	child.Thread = append(child.Thread, note)
	child.Bytes += len(note)
	return child
}

// MergeSnapshots 把父快照与子 agent 特有的指令组合起来。
// 每层按父级在前的顺序拼接，按条目内容精确去重（保留首次出现），
// 然后重新限界并计算新的 Bytes。
func MergeSnapshots(parent, additional InstructionSnapshot) InstructionSnapshot {
	user := mergeLayer(parent.User, additional.User)
	project := mergeLayer(parent.Project, additional.Project)
	thread := mergeLayer(parent.Thread, additional.Thread)
	return normalizeBudget(user, project, thread, MaxSnapshotBytes)
}

// mergeLayer 把 b 的条目追加到 a，跳过任何已存在的内容。
func mergeLayer(a, b []string) []string {
	out := make([]string, 0, len(a)+len(b))
	out = append(out, a...)
	seen := make(map[string]bool, len(out))
	for _, v := range out {
		seen[v] = true
	}
	for _, v := range b {
		if !seen[v] {
			out = append(out, v)
			seen[v] = true
		}
	}
	return out
}

// normalizeBudget 是清洗并限界各层的唯一入口。它始终分配新切片，
// 使每个公开构造函数都获得类似 Clone 的隔离性。条目按 All() 顺序处理；
// 大于剩余空间的条目会在 rune 边界处裁剪，空/仅空白的条目会被丢弃。
func normalizeBudget(user, project, thread []string, budget int) InstructionSnapshot {
	if budget < 0 {
		budget = 0
	}
	var out [3][]string
	layers := [3][]string{user, thread, project} // All() 的顺序
	used := 0
	for li, entries := range layers {
		for _, raw := range entries {
			entry := scrubEntry(raw)
			if entry == "" || used >= budget {
				continue
			}
			entry = clipRunes(entry, budget-used)
			if entry == "" {
				continue
			}
			out[li] = append(out[li], entry)
			used += len(entry)
		}
	}
	return InstructionSnapshot{
		User:    out[0],
		Thread:  out[1],
		Project: out[2],
		Bytes:   used,
	}
}

// scrubEntry 是最小且确定的清洗：丢弃 NUL、C0 控制字符（制表符/换行/回车除外）和 DEL，
// 然后修剪两端空白。它有意只存储纯文本，不尝试对机密信息脱敏——
// 那属于调用方的指令解析和审计层。WriteRune 还会把畸形 UTF-8 规范化为 U+FFFD，
// 因此结果在裁剪前始终是合法 UTF-8。
func scrubEntry(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == 0:
			continue
		case r == 0x7f:
			continue
		case r < 0x20 && r != '\t' && r != '\n' && r != '\r':
			continue
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// clipRunes 把 s 缩短到至多 limit 字节，且不切断 UTF-8 rune。
// 调用方保证 s 是合法 UTF-8 且 len(s) > limit > 0。
func clipRunes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(s) <= limit {
		return s
	}
	k := limit
	for k > 0 && !utf8.RuneStart(s[k]) {
		k--
	}
	return s[:k]
}

// cloneStrings 复制字符串切片；nil 保持为 nil，这样空层在往返时无需分配。
func cloneStrings(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}
