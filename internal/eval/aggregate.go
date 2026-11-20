package eval

import (
	"strings"
	"time"
	"unicode"
)

// Summary 是一组已评分 Result 的与顺序无关的聚合。
// 它是 AggregateScores 产生的值；调用方应将其视为
// 只读（特别是 FailureCounts map 归 Summary 所有）。
type Summary struct {
	// Total 是交给 AggregateScores 的结果数。
	Total int `json:"total"`
	// Valid 是被聚合的结构健全的结果数。
	Valid int `json:"valid"`
	// Skipped 是被忽略的无效结果数（Total - Valid）。
	Skipped int `json:"skipped"`
	// Passed 和 Failed 只统计有效结果。
	Passed int `json:"passed"`
	Failed int `json:"failed"`
	// PassRate 是 Passed*100/Valid，范围为 [0,100]。Valid == 0 时为 0，
	// 绝不是 NaN 或 +Inf。
	PassRate float64 `json:"pass_rate"`
	// AverageDuration 是有效结果的平均 Duration。Valid == 0 时为
	// 0。没有计时数据的有效结果（Duration == 0）计
	// 作零，因此均值与通过率基于同一集合。
	AverageDuration time.Duration `json:"average_duration"`
	// FailureCounts 按规范化原因对失败的有效结果分组。
	// 键是 rune 安全的（保留 CJK 和 emoji）、去除空白的
	// 原因，截断到 failureReasonKeyRunes 个 rune；map 从不为 nil。
	FailureCounts map[string]int `json:"failure_counts"`
}

// failureReasonKeyRunes 限制 FailureCounts 键的长度而不
// 曾切开 UTF-8 rune（UTF-8 原理见 clip）。
const failureReasonKeyRunes = 80

// valid 报告 Result 是否结构健全到足以聚合。
// 来自 Run 的结果带有 1 基 Index、非负的轮数和非负的时长；其他任何情况都是畸形记录并被忽略。
// 非负时长；其他任何情况都是畸形记录并被忽略。
func (r Result) valid() bool {
	return r.Index > 0 && r.Turns >= 0 && r.Duration >= 0
}

// AggregateScores 将结果折叠为 Summary。它是纯函数：从不修改
// 结果，且只依赖有效结果的多重集，而非其
// 结果，且只依赖有效结果的多重集，而非其顺序。因此并发收集的结果可以按任意顺序传入。
func AggregateScores(results []Result) Summary {
	s := Summary{
		Total:         len(results),
		FailureCounts: make(map[string]int),
	}
	var durationSum time.Duration
	for _, r := range results {
		if !r.valid() {
			s.Skipped++
			continue
		}
		s.Valid++
		if r.Passed {
			s.Passed++
		} else {
			s.Failed++
			s.FailureCounts[failureReasonKey(r.Reason)]++
		}
		durationSum += r.Duration
	}
	s.PassRate = passRate(s.Passed, s.Valid)
	if s.Valid > 0 {
		s.AverageDuration = durationSum / time.Duration(s.Valid)
	}
	return s
}

// failureReasonKey 将失败原因规范化为稳定、rune 安全的 map
// 键。先移除所有 Unicode 空白，使仅在间距上不同的原因
// 键。先移除所有 Unicode 空白，使仅在间距上不同的原因（CJK 文本常见）落入同一桶；然后 clip
// 按 rune 截断，因此 CJK 和 emoji 从不在 rune 中间被切开，键保持有效 UTF-8。
// 保持有效 UTF-8。
func failureReasonKey(reason string) string {
	stripped := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, reason)
	return clip(stripped, failureReasonKeyRunes)
}
