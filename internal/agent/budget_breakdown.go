package agent

import "sort"

// BudgetBlock 是提示预算的一个归属分片。Name 标识贡献者
// （"system"、"history" 或原样传入的文件字符串），Tokens 是其考虑 CJK 的估算开销，
// Percent 是它在整个明细中的占比，取值范围 [0, 100]。
type BudgetBlock struct {
	Name    string
	Tokens  int
	Percent float64
}

// 用于两个非文件贡献者的标签。文件分片直接使用传入的字符串作为 Name，
// 这样调用方就能识别出占用 token 的文件（或带标签的仓库映射条目）。
const (
	BudgetBlockSystem  = "system"
	BudgetBlockHistory = "history"
)

// dominantBlockPercent 是判定某个分片"主导"提示所需严格超过的占比阈值。
// 恰好 50% 不算主导。
const dominantBlockPercent = 50.0

// BudgetBreakdown 把估算的提示大小归因到各贡献者：
// 系统提示、对话历史以及每个传入的文件。token 开销来自 EstimateTokens，
// 因此宽字符（CJK）能被正确计数。
//
// 结果按 Tokens 降序排序（相同则按 Name 升序），使占用最多者排在前面。
// Percent 是该分片占所有分片 Tokens 总和的比例；没有可统计内容时返回空切片。
// 空的 system/history 字符串和空的文件条目会被跳过。该函数是纯函数：
// 不读取任何状态、没有依赖，也不会修改调用方的输入。
func BudgetBreakdown(system, history string, files []string) []BudgetBlock {
	type candidate struct {
		name string
		text string
	}
	candidates := make([]candidate, 0, 2+len(files))
	if system != "" {
		candidates = append(candidates, candidate{BudgetBlockSystem, system})
	}
	if history != "" {
		candidates = append(candidates, candidate{BudgetBlockHistory, history})
	}
	for _, file := range files {
		if file != "" {
			candidates = append(candidates, candidate{file, file})
		}
	}

	blocks := make([]BudgetBlock, 0, len(candidates))
	total := 0
	for _, c := range candidates {
		tokens := EstimateTokens(c.text)
		total += tokens
		blocks = append(blocks, BudgetBlock{Name: c.name, Tokens: tokens})
	}
	if total == 0 {
		return blocks
	}
	for i := range blocks {
		blocks[i].Percent = float64(blocks[i].Tokens) / float64(total) * 100
	}

	sort.Slice(blocks, func(i, j int) bool {
		if blocks[i].Tokens != blocks[j].Tokens {
			return blocks[i].Tokens > blocks[j].Tokens
		}
		return blocks[i].Name < blocks[j].Name
	})
	return blocks
}

// DominantBlock 在最大分片占据超过一半估算 token 时将其返回，否则返回 false。
// 分片通常应由 BudgetBreakdown 排好序，但这里通过扫描求最大值，
// 因此该辅助函数对任意输入顺序都正确。空明细或最大分片恰好占 50% 时返回 false。
// 这是可选的 "largest: <name>" 提示的纯逻辑基础；不会修改任何现有提醒文本。
func DominantBlock(blocks []BudgetBlock) (BudgetBlock, bool) {
	var best BudgetBlock
	found := false
	for _, block := range blocks {
		if !found || block.Tokens > best.Tokens {
			best, found = block, true
		}
	}
	if !found {
		return BudgetBlock{}, false
	}
	percent := best.Percent
	if percent == 0 && best.Tokens > 0 {
		// 兼容只提供了 token 数却没有百分比的调用方。
		total := 0
		for _, block := range blocks {
			total += block.Tokens
		}
		if total > 0 {
			percent = float64(best.Tokens) / float64(total) * 100
		}
	}
	return best, percent > dominantBlockPercent
}
