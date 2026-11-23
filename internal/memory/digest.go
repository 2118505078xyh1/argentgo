package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"sort"
)

// 指令文件的摘要快照，用于会话内检测规则文件在轮次之间被修改。
//
// 每个文档按路径（规范化键）记录正文的 SHA-256 与一份有界的正文副本，
// 使得差异比较既能报告「哪些路径变了」，也能携带新内容供重注入。整个集合
// 另有一个稳定的 Version 摘要，便于快速判断「无变化」这一常见情况。
// 本文件只做纯计算：不访问磁盘、不注入提示、不做脱敏。

// maxInstructionTextBytes 是快照中每份正文副本的上界。它与 DefaultLimit 对齐：
// 加载器本身已按该上限裁剪，这里再钳一次，防止调用方传入未裁剪的文档。
const maxInstructionTextBytes = DefaultLimit

// instructionEntry 是一条路径在快照中的内容摘要与正文副本。
type instructionEntry struct {
	path   string // 清理后的原始路径，用于展示
	digest string
	text   string
}

// InstructionState 是一组指令文档在某时刻的不可变摘要快照。零值可用。
type InstructionState struct {
	entries map[string]instructionEntry
	version string
}

// SnapshotInstructions 为 docs 构建摘要快照。路径按规范化键去重（Windows 上
// 大小写折叠），正文按 rune 安全裁剪到上界。空白正文会被跳过。
func SnapshotInstructions(docs []Doc) InstructionState {
	entries := make(map[string]instructionEntry, len(docs))
	for _, doc := range docs {
		key := normalizePath(doc.Path)
		if key == "" || key == "." {
			continue
		}
		text := doc.Text
		if len(text) > maxInstructionTextBytes {
			text = clipRunes(text, maxInstructionTextBytes)
		}
		if text == "" {
			continue
		}
		sum := sha256.Sum256([]byte(text))
		entries[key] = instructionEntry{path: filepath.Clean(doc.Path), digest: hex.EncodeToString(sum[:]), text: text}
	}
	return InstructionState{entries: entries, version: digestVersion(entries)}
}

// Version 返回整个集合的稳定摘要；空集合返回空串。调用方可用它做快速相等判断。
func (s InstructionState) Version() string { return s.version }

// IsZero 报告快照是否为空（没有可比较的指令文件）。
func (s InstructionState) IsZero() bool { return len(s.entries) == 0 }

// InstructionChangeKind 描述单条指令相对基线发生的变化。
type InstructionChangeKind string

const (
	InstructionAdded   InstructionChangeKind = "added"
	InstructionUpdated InstructionChangeKind = "updated"
	InstructionRemoved InstructionChangeKind = "removed"
)

// InstructionChange 是一条指令变化。Text 在 added/updated 时携带新正文，
// removed 时为空；Path 是清理后的原始路径（供展示）。
type InstructionChange struct {
	Path string
	Kind InstructionChangeKind
	Text string
}

// DiffInstructionStates 比较 before 与 after，返回按路径排序的确定性变化列表。
func DiffInstructionStates(before, after InstructionState) []InstructionChange {
	beforePaths := make([]string, 0, len(before.entries))
	for path := range before.entries {
		beforePaths = append(beforePaths, path)
	}
	sort.Strings(beforePaths)
	afterPaths := make([]string, 0, len(after.entries))
	for path := range after.entries {
		afterPaths = append(afterPaths, path)
	}
	sort.Strings(afterPaths)

	var out []InstructionChange
	i, j := 0, 0
	for i < len(beforePaths) || j < len(afterPaths) {
		switch {
		case i >= len(beforePaths):
			key := afterPaths[j]
			e := after.entries[key]
			out = append(out, InstructionChange{Path: e.path, Kind: InstructionAdded, Text: e.text})
			j++
		case j >= len(afterPaths):
			key := beforePaths[i]
			out = append(out, InstructionChange{Path: before.entries[key].path, Kind: InstructionRemoved})
			i++
		case beforePaths[i] < afterPaths[j]:
			key := beforePaths[i]
			out = append(out, InstructionChange{Path: before.entries[key].path, Kind: InstructionRemoved})
			i++
		case beforePaths[i] > afterPaths[j]:
			key := afterPaths[j]
			e := after.entries[key]
			out = append(out, InstructionChange{Path: e.path, Kind: InstructionAdded, Text: e.text})
			j++
		default:
			key := beforePaths[i]
			if before.entries[key].digest != after.entries[key].digest {
				e := after.entries[key]
				out = append(out, InstructionChange{Path: e.path, Kind: InstructionUpdated, Text: e.text})
			}
			i++
			j++
		}
	}
	return out
}

// digestVersion 对「路径 + 摘要」的有序序列做 SHA-256，得到集合级版本摘要。
func digestVersion(entries map[string]instructionEntry) string {
	if len(entries) == 0 {
		return ""
	}
	paths := make([]string, 0, len(entries))
	for path := range entries {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, path := range paths {
		h.Write([]byte(path))
		h.Write([]byte{0})
		h.Write([]byte(entries[path].digest))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
