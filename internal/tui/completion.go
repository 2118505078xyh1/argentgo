package tui

import (
	"sort"
	"unicode"
)

// slash 命令补全的候选表原语。当前没有任何调用方，留作侧栏/输入行日后
// 展示 "/xxx" 候选项的基建。它不 import internal/commands（依赖规则禁止
// tui 反向依赖上层包），候选由调用方以 [CompletionCandidate] 注入。
//
// 设计要点：
//   - 纯内存、零外部依赖；只依赖标准库的 sort 与 unicode。
//   - 候选按 Command 字典序稳定排序，结果对同一输入永远确定。
//   - 大小写不敏感的前缀过滤按 rune 进行，因此 CJK/emoji 安全。
//   - 绝不修改调用方传入的切片：构造时先拷贝再排序。
//
// candidates 保存完整的已排序主表；query 是当前过滤词；index 是候选在
// "过滤后的可见列表" 中的下标。过滤以惰性方式在 Count/Current 中按 query
// 计算，无需在主表之外再维护一份过滤结果，Reset 清掉 query 即自然复原。
type CompletionCandidate struct {
	Command     string
	Description string
}

// CompletionState 持有一组有序补全候选及当前查询/选中位置。
// 零值可用：没有候选，Count 为 0，Current 返回 false。
type CompletionState struct {
	query      string
	candidates []CompletionCandidate
	index      int
}

// NewCompletionState 用 cands 的拷贝构造补全状态，并按 Command 字典序
// 稳定排序。cands 为 nil 或空时得到一个合法但无候选的状态；传入切片
// 本身不会被排序或修改。
func NewCompletionState(cands []CompletionCandidate) *CompletionState {
	s := &CompletionState{}
	if len(cands) == 0 {
		return s
	}
	s.candidates = append([]CompletionCandidate(nil), cands...)
	sort.SliceStable(s.candidates, func(i, j int) bool {
		return s.candidates[i].Command < s.candidates[j].Command
	})
	return s
}

// Query 设置过滤词并把选中位置重置到第一个可见候选。过滤按 Command 的
// rune 前缀进行、大小写不敏感；空 q 匹配全部候选。
func (s *CompletionState) Query(q string) {
	if s == nil {
		return
	}
	s.query = q
	s.index = 0
}

// Count 返回当前 query 下可见候选的数量。
func (s *CompletionState) Count() int {
	if s == nil {
		return 0
	}
	n := 0
	for _, c := range s.candidates {
		if prefixMatch(s.query, c.Command) {
			n++
		}
	}
	return n
}

// Current 返回当前选中的候选。没有可见候选（或 index 越界）时返回零值与
// false。
func (s *CompletionState) Current() (CompletionCandidate, bool) {
	if s == nil {
		return CompletionCandidate{}, false
	}
	i := 0
	for _, c := range s.candidates {
		if !prefixMatch(s.query, c.Command) {
			continue
		}
		if i == s.index {
			return c, true
		}
		i++
	}
	return CompletionCandidate{}, false
}

// Next 把选中位置前移一位，到末尾后回绕到开头。没有可见候选时无操作。
func (s *CompletionState) Next() {
	if s == nil {
		return
	}
	n := s.Count()
	if n == 0 {
		return
	}
	s.index = (s.index + 1) % n
}

// Prev 把选中位置后移一位，到开头后回绕到末尾。没有可见候选时无操作。
func (s *CompletionState) Prev() {
	if s == nil {
		return
	}
	n := s.Count()
	if n == 0 {
		return
	}
	s.index = (s.index - 1 + n) % n
}

// Reset 清空过滤词并把选中位置归零，使全部候选重新可见。
func (s *CompletionState) Reset() {
	if s == nil {
		return
	}
	s.query = ""
	s.index = 0
}

// prefixMatch 报告 command 是否以 query 为前缀，大小写不敏感。
// 双方先转成 []rune 再逐位用 unicode.ToLower 折叠：rune 索引与字节位置
// 无关，故 CJK/emoji 不会被从中间截断；ToLower 对 rune 是 1:1 映射，
// 折叠后索引仍然对齐。空 query 是任意字符串的前缀。
func prefixMatch(query, command string) bool {
	q, c := []rune(query), []rune(command)
	if len(q) > len(c) {
		return false
	}
	for i, r := range q {
		if unicode.ToLower(r) != unicode.ToLower(c[i]) {
			return false
		}
	}
	return true
}
