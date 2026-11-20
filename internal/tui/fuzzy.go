package tui

// 用于侧边栏定位器（会话、
// 工作区、任务）的带评分的模糊子序列匹配器。仅用标准库；UTF-8/CJK 安全，因为
// 调用方看到的每个索引都是原始文本中的 rune 索引。
//
// 该实现是一个小型动态规划：它找到把 pattern 的 rune
// 分配给 text 的 rune 的子序列，使由
// 匹配奖励、词边界奖励、连续段奖励和间隔
// 惩罚构成的得分最大化。它有意避开 fzf 的 Slab/charClass 微优化；
// 面板最多容纳几百个短字符串，因此这里清晰性优先。

import (
	"sort"
	"unicode"
)

// 评分权重。它们是相对值；测试比较的是结果，而非绝对值。
const (
	fuzzyScoreMatch       = 16 // a pattern rune matched a text rune
	fuzzyScoreFirst       = 16 // the match landed on the very first text rune
	fuzzyScoreBoundary    = 8  // word boundary (space, separator, camel, digit run)
	fuzzyScorePathSep     = 4  // extra boundary weight for a path separator in pathMode
	fuzzyScoreConsecutive = 8  // this rune directly follows the previous matched rune
	fuzzyGapStart         = -3 // penalty for opening a gap of skipped runes
	fuzzyGapExtend        = -1 // penalty for each additional skipped rune
)

const fuzzyNegInf = -1 << 30

// FuzzyResult 是一个带评分的候选。调用方从 FuzzyMatch 调用构建它，
// 并让 Index 指向自己的条目，使排序不会丢失
// 关联。SortFuzzyResults 只看这四个字段。
type FuzzyResult struct {
	Score     int
	Positions []int // rune indices of the matched characters, ascending
	TextLen   int   // rune count of the matched text (shorter wins a tie)
	Index     int   // caller-supplied source index; final deterministic tiebreak
}

// FuzzyMatch 报告 pattern 的每个 rune 是否按顺序出现在 text 中，
// 若是则给出最佳得分和匹配到的 rune 索引。
//
// 大小写处理是 smart-case：当 pattern 不含大写 rune 时，
// 比较不区分大小写（双方都用 unicode.ToLower 折叠）；出现
// 任何大写 rune 则比较区分大小写。空的
// pattern 匹配所有内容，得分为 0 且无位置。
//
// pathMode 把 '/', '\\', ':' 和 ',' 当作词分隔符（除了
// 始终作为分隔符的 '.'、'_' 和 '-'），并给这些分隔符额外的
// 边界奖励，从而偏好从路径组件开始的匹配。
func FuzzyMatch(pattern, text string, pathMode bool) (score int, positions []int, ok bool) {
	patOrig := []rune(pattern)
	txtOrig := []rune(text)
	P, T := len(patOrig), len(txtOrig)
	if P == 0 {
		return 0, nil, true
	}
	if P > T {
		return 0, nil, false
	}

	// 智能大小写：当 pattern 全为小写时把双方折叠一次。
	// unicode.ToLower 对 rune 是 1:1 的，因此折叠后的索引对 txtOrig 仍有效。
	fold := !containsUpper(patOrig)
	pat, txt := patOrig, txtOrig
	if fold {
		pat = lowerRunes(patOrig)
		txt = lowerRunes(txtOrig)
	}

	// back[i][j] 是当 pat[i] 匹配到 txt[j] 时，紧接 txt[j] 之前匹配的
	// rune 的文本索引；-1 表示"没有前一个 rune"（i == 0）。
	back := make([][]int, P)

	// prev* 保存 pat[i-1] 匹配在每个文本位置时的最佳得分。
	prevScore := make([]int, T)
	prevValid := make([]bool, T)
	for j := 0; j < T; j++ {
		if pat[0] == txt[j] {
			prevScore[j] = charBonus(txtOrig, j, pathMode) + gapCost(j)
			prevValid[j] = true
		}
	}

	curScore := make([]int, T)
	curValid := make([]bool, T)
	for i := 1; i < P; i++ {
		// prevScore[k] - k*gapExtend（k <= j）的运行最大值。这把
		// "最佳较早匹配加上间隔"的搜索变成每个文本位置 O(1)。
		bestVal := make([]int, T)
		bestK := make([]int, T)
		runVal, runK := fuzzyNegInf, -1
		for j := 0; j < T; j++ {
			if prevValid[j] {
				v := prevScore[j] - j*fuzzyGapExtend
				if v > runVal {
					runVal, runK = v, j
				}
			}
			bestVal[j], bestK[j] = runVal, runK
		}

		back[i] = make([]int, T)
		for j := 0; j < T; j++ {
			curValid[j] = false
			if pat[i] != txt[j] {
				continue
			}
			base := charBonus(txtOrig, j, pathMode)
			best, bk := fuzzyNegInf, -1

			// 连续匹配：前一个 rune 在 j-1，无间隔惩罚。
			if j >= 1 && prevValid[j-1] {
				best = prevScore[j-1] + base + fuzzyScoreConsecutive
				bk = j - 1
			}
			// 有间隔的匹配：前一个 rune 在 k <= j-2。
			if j >= 2 && bestVal[j-2] > fuzzyNegInf/2 {
				cand := bestVal[j-2] + base + fuzzyGapStart + (j-2)*fuzzyGapExtend
				if cand > best {
					best, bk = cand, bestK[j-2]
				}
			}
			if bk >= 0 {
				curScore[j], curValid[j], back[i][j] = best, true, bk
			}
		}
		prevScore, curScore = curScore, prevScore
		prevValid, curValid = curValid, prevValid
	}

	bestEnd, bestEndScore := -1, fuzzyNegInf
	for j := 0; j < T; j++ {
		if prevValid[j] && prevScore[j] > bestEndScore {
			bestEnd, bestEndScore = j, prevScore[j]
		}
	}
	if bestEnd < 0 {
		return 0, nil, false
	}

	positions = make([]int, P)
	k := bestEnd
	for i := P - 1; i >= 0; i-- {
		positions[i] = k
		if i > 0 {
			k = back[i][k]
		}
	}
	return bestEndScore, positions, true
}

// SortFuzzyResults 按得分降序、然后较短文本、然后
// 较早的首次匹配位置、然后调用方 Index 排序。sort.SliceStable 让
// 完全相同的条目保持原有顺序，因此结果对
// 任何输入都是确定的。
func SortFuzzyResults(results []FuzzyResult) {
	sort.SliceStable(results, func(i, j int) bool {
		a, b := results[i], results[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.TextLen != b.TextLen {
			return a.TextLen < b.TextLen
		}
		ai, bi := startOf(a.Positions), startOf(b.Positions)
		if ai != bi {
			return ai < bi
		}
		return a.Index < b.Index
	})
}

// startOf 返回第一个匹配的 rune 索引，或对一个空
// 位置列表返回一个很大的哨兵值，使这类结果在"较早起始"规则下排最后。
func startOf(positions []int) int {
	if len(positions) == 0 {
		return 1 << 30
	}
	return positions[0]
}

// charBonus 是匹配 txt[j] 的与位置无关的部分：一个固定
// 匹配分加上首 rune 奖励和/或词边界奖励。
func charBonus(txtOrig []rune, j int, pathMode bool) int {
	bonus := fuzzyScoreMatch
	if j == 0 {
		bonus += fuzzyScoreFirst
	}
	if boundary, pathSep := isBoundary(txtOrig, j, pathMode); boundary {
		bonus += fuzzyScoreBoundary
		if pathSep {
			bonus += fuzzyScorePathSep
		}
	}
	return bonus
}

// gapCost 是在两个匹配 rune 之间（或在首次匹配之前）跳过 n 个 rune 的
// 惩罚。间隔越长，代价渐进增加。
func gapCost(n int) int {
	if n <= 0 {
		return 0
	}
	return fuzzyGapStart + (n-1)*fuzzyGapExtend
}

// isBoundary 报告 j 处的 rune 是否开启新词：在空白之后、
// 在分隔符之后、在 camelCase 大写处，或在数字/非数字切换处。
// 第二个返回值仅在 pathMode 下对路径分隔符为 true。
func isBoundary(txt []rune, j int, pathMode bool) (boundary, pathSep bool) {
	if j <= 0 || j >= len(txt) {
		return false, false
	}
	prev, cur := txt[j-1], txt[j]
	if unicode.IsSpace(prev) {
		return true, false
	}
	if fuzzySeparator(prev, pathMode) {
		return true, pathMode && isPathSeparator(prev)
	}
	if unicode.IsUpper(cur) && !unicode.IsUpper(prev) {
		return true, false
	}
	if unicode.IsDigit(cur) != unicode.IsDigit(prev) {
		return true, false
	}
	return false, false
}

func fuzzySeparator(r rune, pathMode bool) bool {
	switch r {
	case '.', '_', '-':
		return true
	case '/', '\\', ':', ',':
		return pathMode
	}
	return false
}

func isPathSeparator(r rune) bool {
	switch r {
	case '/', '\\', ':', ',':
		return true
	}
	return false
}

func containsUpper(rs []rune) bool {
	for _, r := range rs {
		if unicode.IsUpper(r) {
			return true
		}
	}
	return false
}

func lowerRunes(rs []rune) []rune {
	out := make([]rune, len(rs))
	for i, r := range rs {
		out[i] = unicode.ToLower(r)
	}
	return out
}
