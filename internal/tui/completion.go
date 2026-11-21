package tui

import (
	"path/filepath"
	"sort"
	"strings"
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

	// 以下四个字段仅由 opt-in 的内联补全（路径 / @文件）写入。
	// slash 补全从不设置它们（保持零值 inline=false），因此在
	// ARGENT_PATH_COMPLETION 未开启时所有既有行为逐字节不变。
	inline     bool // 当前状态是否为内联补全（接受逻辑据此替换词而非整行）
	tokenStart int  // 被补全词在输入文本中的 rune 起始位置
	cursor     int  // 构建状态时光标的 rune 位置（用于接受时保留后缀）
	quoted     bool // 被补全词是否位于已打开的引号内（接受时避免重复加引号）
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
// false。选中下标指向 orderedVisible 的可见顺序，与面板渲染保持一致。
func (s *CompletionState) Current() (CompletionCandidate, bool) {
	if s == nil {
		return CompletionCandidate{}, false
	}
	visible := s.orderedVisible()
	if s.index < 0 || s.index >= len(visible) {
		return CompletionCandidate{}, false
	}
	return visible[s.index], true
}

// At 返回当前 query 下第 i 个可见候选（0 基），不修改选中位置。
// i 越界或没有候选时返回零值与 false；顺序同 orderedVisible。
func (s *CompletionState) At(i int) (CompletionCandidate, bool) {
	if s == nil || i < 0 {
		return CompletionCandidate{}, false
	}
	visible := s.orderedVisible()
	if i >= len(visible) {
		return CompletionCandidate{}, false
	}
	return visible[i], true
}

// Visible 返回当前 query 下全部可见候选的副本，顺序与 Current/At 一致。
// 它不修改 query 或选中位置，供面板逐行渲染使用。
func (s *CompletionState) Visible() []CompletionCandidate {
	return s.orderedVisible()
}

// orderedVisible 返回当前 query 下的可见候选并按展示顺序排列：前缀命中
// 优先（保持主表字典序），其余非前缀命中按 FuzzyMatch 分数降序。它只决定
// 顺序，过滤语义仍由 matchCommand 保证，且不修改主表 s.candidates。
//
// 默认帧不变：空 query、候选不足两条、或全部命中都是前缀时，直接按主表
// 字典序返回，与既有行为逐字节一致。只有 query 非空且存在非前缀命中时，
// 才对这部分候选按分数排序——这正是"模糊查询且多候选"需要区分优劣的场景。
func (s *CompletionState) orderedVisible() []CompletionCandidate {
	if s == nil {
		return nil
	}
	q := string(lowerRunes([]rune(s.query)))
	rank := s.query != "" && len(s.candidates) >= 2

	var prefix, fuzzy []CompletionCandidate
	for _, c := range s.candidates {
		if !matchCommand(s.query, c.Command) {
			continue
		}
		if rank && !prefixMatch(q, c.Command) {
			fuzzy = append(fuzzy, c)
		} else {
			prefix = append(prefix, c)
		}
	}
	if len(fuzzy) == 0 {
		out := make([]CompletionCandidate, 0, len(prefix))
		out = append(out, prefix...)
		return out
	}

	// 复用 fuzzy.go 的 SortFuzzyResults：Score 降序、短文本优先、早匹配
	// 优先、原始下标兜底，且完全相同的条目保持稳定。fuzzy 是按主表顺序
	// 收集的，故其下标即字典序 tiebreak，排序后可据此映射回候选。
	results := make([]FuzzyResult, 0, len(fuzzy))
	for i, c := range fuzzy {
		score, positions, ok := FuzzyMatch(q, c.Command, false)
		if !ok {
			// matchCommand 已确认命中，这里仅做保守兜底。
			score, positions = 0, nil
		}
		results = append(results, FuzzyResult{
			Score:     score,
			Positions: positions,
			TextLen:   len([]rune(c.Command)),
			Index:     i,
		})
	}
	SortFuzzyResults(results)

	out := make([]CompletionCandidate, 0, len(prefix)+len(fuzzy))
	out = append(out, prefix...)
	for _, r := range results {
		out = append(out, fuzzy[r.Index])
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

// ============================================================================
// 以下为 opt-in 的内联补全（文件路径 / @文件）基建。仅使用标准库，
// 候选源是注入的 FileSystem（与工作区面板共用），绝不自行读环境变量或
// 触碰真实文件系统；由调用方（Model）在 ARGENT_PATH_COMPLETION=1 时启用。
// ============================================================================

// inlineCompletionKind 区分内联补全的候选源。
type inlineCompletionKind int

const (
	inlineNone inlineCompletionKind = iota // 不是内联补全上下文
	inlineAt                               // 含 @ 前缀的文件提及
	inlinePath                             // 含路径分隔符的文件路径片段
)

// activeInlineWord 返回光标前正在补全的词、其 rune 起始位置，以及词是否
// 位于已打开的引号内。
//
// "引号感知"：向左扫描时忽略空白、找最近的引号；一旦找到，词从引号后开始，
// 因而可包含空格（带空格的路径只有在引号内才能作为一个整体被补全）。
// 找不到引号时退回到最近空白之后，保持普通单词语义。
func activeInlineWord(text string, cursor int) (word string, start int, quoted bool) {
	runes := []rune(text)
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(runes) {
		cursor = len(runes)
	}
	// 向左找最近的单/双/反引号（跨过空白）。找到即视为引号内的词。
	for i := cursor - 1; i >= 0; i-- {
		r := runes[i]
		if r == '"' || r == '\'' || r == '`' {
			start = i + 1
			// 去掉可能的前导空白，使紧随闭合引号的普通词也能正常分类。
			for start < cursor && unicode.IsSpace(runes[start]) {
				start++
			}
			return string(runes[start:cursor]), start, true
		}
	}
	// 无引号：退回到最近空白之后。
	start = cursor
	for start > 0 && !unicode.IsSpace(runes[start-1]) {
		start--
	}
	return string(runes[start:cursor]), start, false
}

// classifyInlineWord 判断词 word 是否构成内联补全上下文。
//
//   - "@..." 触发 @文件补全；
//   - 含 '/' 或 '\\' 的词触发路径补全，但行首单独的 "/xxx"（slash 命令）
//     除外——它必须交回既有 slash 补全，以免开启门禁后抢占命令补全。
func classifyInlineWord(word string, start int) inlineCompletionKind {
	if word == "" {
		return inlineNone
	}
	if strings.HasPrefix(word, "@") {
		return inlineAt
	}
	if strings.ContainsAny(word, `/\`) {
		if start == 0 && strings.HasPrefix(word, "/") {
			rest := strings.TrimPrefix(word, "/")
			if !strings.ContainsAny(rest, `/\`) {
				return inlineNone
			}
		}
		return inlinePath
	}
	return inlineNone
}

// quoteIfNeeded 返回适合写入输入行的路径文本。路径含空白、引号或括号等
// 需要保护的字符时用双引号包裹（内部双引号转义），否则原样返回。
// 已经成对双引号包裹的字符串不再重复包裹。
func quoteIfNeeded(s string) string {
	if !strings.ContainsAny(s, " \t\r\n\"'`()[]{}") {
		return s
	}
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

// pathCompletionCandidates 根据路径片段 frag 在 fs 中生成补全候选。
//
// frag 被拆成目录部分与文件名基部分：目录经 filepath.Join 定位到工作区，
// 基部分以 filepath.Match 的 "*" 通配做前缀过滤（等价于 filepath.Glob，
// 但走注入的 fs 以便测试且不依赖真实磁盘）。结果是相对 root、用 '/' 分隔
// 的原始路径；目录项以 '/' 结尾。引号包裹推迟到接受时按上下文决定。
func pathCompletionCandidates(fs FileSystem, root, frag string) []CompletionCandidate {
	if fs == nil || root == "" || frag == "" {
		return nil
	}
	dirPart, base := "", frag
	if i := strings.LastIndexAny(frag, `/\`); i >= 0 {
		dirPart, base = frag[:i], frag[i+1:]
	}
	dir := root
	if dirPart != "" {
		dir = filepath.Join(root, filepath.FromSlash(dirPart))
	}
	entries, err := fs.ReadDir(dir)
	if err != nil {
		return nil
	}
	pattern := base + "*"
	var out []CompletionCandidate
	for _, e := range entries {
		if matched, err := filepath.Match(pattern, e.Name); err != nil || !matched {
			continue
		}
		rel := e.Name
		if dirPart != "" {
			rel = filepath.ToSlash(filepath.Join(filepath.FromSlash(dirPart), e.Name))
		}
		if e.IsDir {
			rel += "/"
		}
		out = append(out, CompletionCandidate{Command: rel})
	}
	return out
}

// atCompletionCandidates 递归列出工作区全部文件，作为 @提及 的补全候选。
// 复用工作区面板的忽略目录与深度/数量上限，避免把 .git 等噪声塞进候选。
func atCompletionCandidates(fs FileSystem, root string) []CompletionCandidate {
	if fs == nil || root == "" {
		return nil
	}
	var out []CompletionCandidate
	var walk func(dir, rel string, depth int)
	walk = func(dir, rel string, depth int) {
		if depth > maxWorkspaceDepth || len(out) >= maxWorkspaceEntries {
			return
		}
		entries, err := fs.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.IsDir && workspaceIgnoredDirs[e.Name] {
				continue
			}
			childRel := e.Name
			if rel != "" {
				childRel = rel + "/" + e.Name
			}
			if e.IsDir {
				walk(filepath.Join(dir, e.Name), childRel, depth+1)
				continue
			}
			out = append(out, CompletionCandidate{Command: childRel})
			if len(out) >= maxWorkspaceEntries {
				return
			}
		}
	}
	walk(root, "", 0)
	return out
}
