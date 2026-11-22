package memory

// 轻量词形还原召回（R52 TODO #50 的一部分）。这是可选的 opt-in 能力：
// Bank.Recall 永远不调用本文件的函数，默认路径逐字节保持原样；只有显式
// 调用 RecallWithStemming 或 Bank.RecallStemming 才会启用形态归一。
// 实现只用标准库，且全程不修改入参、不做磁盘 I/O（Bank 方法仅在取数时读盘）。

import (
	"sort"
	"strings"
)

// minStemLength 是参与形态还原的最短词长。短于该长度的 token（is、as、
// us、gas、bus 等）一律原样返回，避免把常见小词截成无意义的片段。
const minStemLength = 4

// stemWord 对一个已经转成小写的 token 做保守的词形还原。规则按"最长后缀
// 优先"依次尝试：ies/ied → y，es → 空，单个 s → 空，ed → 空，ing → 空。
//
// 保守性体现在三处，专门防止 1Password 被还原成 pass 之类的误匹配：
//   - 只处理纯 ASCII 小写字母组成的 token；含数字（1password）或任何
//     非字母字符的 token 原样返回；
//   - 去掉后缀后必须至少保留 3 个字符（ies/ied 保留 2 个再加 y），
//     因此 "is"/"as"/"bus" 不会变成单字母；
//   - 单个 s 只在前面不是 s/u 时剥离，所以 pass、class、plus 不动。
//
// 汉字、假名、韩文等 CJK token 是多字节 UTF-8，含非 ASCII 字节，
// 因此天然不走任何规则——中文不涉及形态变化。
func stemWord(word string) string {
	n := len(word)
	if n < minStemLength {
		return word
	}
	for i := 0; i < n; i++ {
		c := word[i]
		if c < 'a' || c > 'z' {
			return word
		}
	}

	switch {
	case stemPrefixOK(word, "ies", 2):
		return word[:n-3] + "y" // policies -> policy
	case stemPrefixOK(word, "ied", 2):
		return word[:n-3] + "y" // denied -> deny
	case stemPrefixOK(word, "es", 3):
		return word[:n-2] // boxes -> box, passes -> pass, watches -> watch
	case word[n-1] == 's' && word[n-2] != 's' && word[n-2] != 'u':
		return word[:n-1] // backups -> backup；pass/class/plus 保持原样
	case stemPrefixOK(word, "ed", 3):
		return word[:n-2] // walked -> walk
	case stemPrefixOK(word, "ing", 3):
		stem := word[:n-3]
		// 双写辅音归一：running -> run、swimming -> swim。
		if len(stem) >= 2 && stem[len(stem)-1] == stem[len(stem)-2] {
			stem = stem[:len(stem)-1]
		}
		return stem
	}
	return word
}

// stemPrefixOK 报告 word 是否以 suffix 结尾，且剥离后缀后至少保留 min
// 个字符，从而把过短的词（"ties"、"going"）排除在规则之外。
func stemPrefixOK(word, suffix string, min int) bool {
	return len(word)-len(suffix) >= min && strings.HasSuffix(word, suffix)
}

// stemmedTokens 复用 bank.go 的 tokenize（小写 + 按标点/CJK 切分），再对
// 每个 token 做词形还原。CJK token 是单字，会原样穿过 stemWord。
func stemmedTokens(text string) []string {
	tokens := tokenize(text)
	out := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		out = append(out, stemWord(tok))
	}
	return out
}

// RecallWithStemming 用还原后的 query / 文档标记做相关性召回，是纯函数：
// 不读写磁盘、不修改 entries，返回的新切片不与入参共享底层数组。
//
// 返回所有命中至少一个 query 标记的条目，按（命中数降序, TS 降序, 输入
// 顺序）排列；空 query 时与 Bank.Recall 一致地返回全部条目并按新近度排序。
// 已作废（Invalidated）的条目和 Bank.Recall 一样被排除。
func RecallWithStemming(query string, entries []Memory) []Memory {
	return recallStemmed(query, entries, nil, 0)
}

// RecallStemming 是 Bank 级别的 opt-in 入口：与 Recall 一样从磁盘加载，
// 但在匹配前对 query 和文档做词形还原。默认 Recall 完全不受影响。
func (b *Bank) RecallStemming(query string, tags []string, limit int) []Memory {
	b.mu.Lock()
	all, err := b.loadLocked()
	b.mu.Unlock()
	if len(all) == 0 {
		return nil
	}
	_ = err // 部分结果仍然有用；截断的尾部会被忽略
	if limit <= 0 {
		limit = defaultRecallLimit
	}
	return recallStemmed(query, all, cleanTags(tags), limit)
}

// recallStemmed 是词形还原召回的共享实现。limit <= 0 表示不裁剪；wantTags
// 非空时按与 Bank.Recall 相同的 tagMatchBonus 规则加分（不是硬过滤）。
func recallStemmed(query string, entries []Memory, wantTags []string, limit int) []Memory {
	if len(entries) == 0 {
		return nil
	}

	queryTokens := stemmedTokens(query)
	hasTags := len(wantTags) > 0
	filtered := len(queryTokens) > 0 || hasTags

	type hit struct {
		mem   Memory
		score int
		idx   int // 文件/输入顺序，用作稳定的新近优先决胜键
	}
	results := make([]hit, 0, len(entries))
	for i, mem := range entries {
		if mem.Invalidated != "" {
			continue
		}
		score := 0
		if len(queryTokens) > 0 {
			docTokens := stemmedTokens(mem.Text)
			for _, q := range queryTokens {
				for _, tok := range docTokens {
					if q == tok {
						score++
						break
					}
				}
			}
		}
		if hasTags {
			for _, want := range wantTags {
				for _, tag := range mem.Tags {
					if strings.EqualFold(want, tag) {
						score += tagMatchBonus
						break
					}
				}
			}
		}
		if filtered && score == 0 {
			continue
		}
		results = append(results, hit{mem: mem, score: score, idx: i})
	}

	sort.SliceStable(results, func(i, j int) bool {
		if results[i].score != results[j].score {
			return results[i].score > results[j].score
		}
		if results[i].mem.TS.Equal(results[j].mem.TS) {
			return results[i].idx > results[j].idx
		}
		return results[i].mem.TS.After(results[j].mem.TS)
	})
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	out := make([]Memory, len(results))
	for i, r := range results {
		out[i] = r.mem
	}
	return out
}
