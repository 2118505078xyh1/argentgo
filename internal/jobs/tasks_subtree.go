package jobs

import (
	"sort"
	"strings"
)

// subtreeSeparator 是作业 ID 的层级分隔符。约定子作业 ID 形如
// "parent/child"，孙作业形如 "parent/child/grandchild"；因此
// 后代与祖先之间以 "/" 分隔，而不是把 ID 当作普通的连续字符串。
const subtreeSeparator = "/"

// tasksInSubtree 返回 parentID 子树内所有后代作业的 ID（不含 parentID 自身）。
//
// JobInfo（即 Job 的别名）目前没有显式的 parent 字段，因此归属判定改用
// ID 的层级前缀约定：当且仅当作业 ID 以 parentID + "/" 开头时，认为它是
// parentID 的后代。带上分隔符是为了避免把 "job-tools" 之类的同前缀 ID
// 误判为 "job" 的后代；同时这种前缀匹配天然覆盖多层子树，例如
// "parent/child/grandchild" 也会被算作 "parent" 的后代（即使中间层
// "parent/child" 不在 jobs 里）。
//
// 返回的 ID 按字节序稳定排序，因此结果与入参顺序无关、与区域设置无关，
// 对含 CJK 字符的 ID 同样确定。parentID 按原样使用（不做裁剪）；传入空
// 字符串时匹配的是以 "/" 开头的 ID，通常没有匹配项。本函数为纯函数：
// 不修改入参，也不持有对入切片的引用；没有后代时返回非 nil 的空切片，
// 便于直接编码为 JSON 的 []。
func tasksInSubtree(jobs []JobInfo, parentID string) []string {
	prefix := parentID + subtreeSeparator
	ids := make([]string, 0)
	for _, job := range jobs {
		if strings.HasPrefix(job.ID, prefix) {
			ids = append(ids, job.ID)
		}
	}
	// 字节序排序对任意 UTF-8 都确定；ID 来自以 ID 为键的 map，正常情况下
	// 不会重复，故无需额外的去重或稳定化。
	sort.Strings(ids)
	return ids
}
