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
//   - 大小写不敏感的模糊（子序列）过滤按 rune 进行，因此 CJK/emoji 安全；
//     原前缀查询是其子集，行为与顺序保持不变。
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
// rune 子序列进行、大小写不敏感；空 q 匹配全部候选。可见顺序始终由主表
// 字典序决定，不按匹配分数重排。
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
		if matchCommand(s.query, c.Command) {
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
		if !matchCommand(s.query, c.Command) {
			continue
		}
		if i == s.index {
			return c, true
		}
		i++
	}
	return CompletionCandidate{}, false
}

// At 返回当前 query 下第 i 个可见候选（0 基），不修改选中位置。
// i 越界或没有候选时返回零值与 false。
func (s *CompletionState) At(i int) (CompletionCandidate, bool) {
	if s == nil || i < 0 {
		return CompletionCandidate{}, false
	}
	n := 0
	for _, c := range s.candidates {
		if !matchCommand(s.query, c.Command) {
			continue
		}
		if n == i {
			return c, true
		}
		n++
	}
	return CompletionCandidate{}, false
}

// Visible 返回当前 query 下全部可见候选的副本，顺序与 Count/Current 一致
// （按 Command 字典序）。它不修改 query 或选中位置，供面板逐行渲染使用。
func (s *CompletionState) Visible() []CompletionCandidate {
	if s == nil {
		return nil
	}
	out := make([]CompletionCandidate, 0, s.Count())
	for _, c := range s.candidates {
		if matchCommand(s.query, c.Command) {
			out = append(out, c)
		}
	}
	return out
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

// Select 把选中位置设为第 i 个可见候选（0 基），越界时钳制到
// [0, Count()-1]。没有可见候选时无操作；供鼠标点击选择使用。
func (s *CompletionState) Select(i int) {
	if s == nil {
		return
	}
	n := s.Count()
	if n <= 0 {
		return
	}
	if i < 0 {
		i = 0
	}
	if i >= n {
		i = n - 1
	}
	s.index = i
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

// matchCommand 报告 command 是否命中查询。它保留旧的大小写不敏感前缀语义，
// 并把它扩展为模糊子序列匹配：原本查不到的非前缀缩写（如 "rv" 对 "review"）
// 现在也能命中。
//
// 实现要点：
//   - query 先逐 rune 用 unicode.ToLower 折叠，使 FuzzyMatch 的 smart-case
//     走"无大写"分支、对双方都折叠，从而维持旧的大小写不敏感行为。
//   - 前缀命中直接返回 true（前缀必是子序列），前缀路径的结果与旧版逐字节一致；
//     非前缀才交给 FuzzyMatch。只看命中与否，不读取分数，故调用方仍按主表
//     字典序决定 Visible 顺序，不按分数重排。
func matchCommand(query, command string) bool {
	q := string(lowerRunes([]rune(query)))
	if prefixMatch(q, command) {
		return true
	}
	_, _, ok := FuzzyMatch(q, command, false)
	return ok
}
