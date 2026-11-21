package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
)

// TemplateSnapshot 是模板名称集合在某个时刻的不可变快照，用于诊断两次目录之间的差异。
// Names 按 UTF-8 字节序稳定排序；Fingerprint 是排序后名称序列的稳定摘要。
type TemplateSnapshot struct {
	Names       []string
	Fingerprint string
}

// SnapshotTemplates 根据给定的名称集合构造快照。
// 它先复制入参（绝不修改调用方的切片），再按 UTF-8 字节序排序 Names。
// Fingerprint 对排序后的序列逐项写入「UTF-8 字节长度 + ":" + 名称」后取 sha256 十六进制摘要：
// 长度前缀消除了拼接歧义（例如 ["ab","c"] 与 ["a","bc"] 摘要不同），排序消除了入参顺序的影响。
// 本函数是纯函数，不修改入参，可从任意 goroutine 并发调用。
func SnapshotTemplates(names []string) TemplateSnapshot {
	sorted := cloneSortedNames(names)
	return TemplateSnapshot{
		Names:       sorted,
		Fingerprint: fingerprintNames(sorted),
	}
}

// DiffSnapshots 按名称比较两个快照，返回 b 相对 a 新增与删除的名称。
// added 为仅存在于 b 的名称，removed 为仅存在于 a 的名称；两者均按 UTF-8 字节序稳定排序。
// 比较按名字集合语义进行（重复名称只计一次），返回的是新切片且不修改任一入参。
// 本函数是纯函数，可从任意 goroutine 并发调用。
func DiffSnapshots(a, b TemplateSnapshot) (added, removed []string) {
	inA := make(map[string]struct{}, len(a.Names))
	for _, name := range a.Names {
		inA[name] = struct{}{}
	}
	inB := make(map[string]struct{}, len(b.Names))
	for _, name := range b.Names {
		inB[name] = struct{}{}
	}

	added = make([]string, 0, len(inB))
	for name := range inB {
		if _, ok := inA[name]; !ok {
			added = append(added, name)
		}
	}
	removed = make([]string, 0, len(inA))
	for name := range inA {
		if _, ok := inB[name]; !ok {
			removed = append(removed, name)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

// cloneSortedNames 返回 names 的副本并按 UTF-8 字节序排序，保留 nil 与空切片的区分。
// Go 字符串的 < 比较即按字节逐一比较，因此 sort.Strings 与 UTF-8 字节序一致。
func cloneSortedNames(names []string) []string {
	if names == nil {
		return nil
	}
	out := make([]string, len(names))
	copy(out, names)
	sort.Strings(out)
	return out
}

// fingerprintNames 按「字节长度前缀 + 名称」编码计算稳定摘要。
// names 应当已排序；编码中每个名称长度为 UTF-8 字节数（Go 的 len(string) 即字节数）。
func fingerprintNames(names []string) string {
	h := sha256.New()
	for _, name := range names {
		fmt.Fprintf(h, "%d:%s", len(name), name)
	}
	return hex.EncodeToString(h.Sum(nil))
}
