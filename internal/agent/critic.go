package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
)

// DefaultMaxRefinementIterations 是 ShouldRefine 在 maxIterations 非正时采用的默认细化轮次上限。
// 对应 R37 OpenHands 研究中的 max_refinement_iterations = 3。
const DefaultMaxRefinementIterations = 3

// ErrInvalidCriticResult 表示模型输出不是可解析的自评 JSON。
// 调用方可用 errors.Is 区分"模型没给出有效自评"与"语法合法但分数偏低"。
var ErrInvalidCriticResult = errors.New("invalid critic result")

// CriticResult 是模型对自身上一轮输出的结构化自评。
//
// Score 落在 [0, 1]（解析时钳制）；Message 是自然语言评语；
// EventIDs 按模型给出的顺序原样保留，可据此回放/复现自评所依据的事件序列。
type CriticResult struct {
	Score    float64  `json:"score"`
	Message  string   `json:"message"`
	EventIDs []string `json:"event_ids"`
}

// ParseCriticResult 解析模型输出的自评 JSON。
//
// 缺失字段按零值处理（不报错）；顶层为空、null 或 JSON 语法/类型不合法时，
// 返回包装了 ErrInvalidCriticResult 的错误。Score 一律钳制到 [0, 1]，NaN 视为 0；
// EventIDs 保持原始顺序，不排序、不去重，以保证可复现。
// 本函数是纯函数，不读取任何全局状态。
func ParseCriticResult(data []byte) (CriticResult, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return CriticResult{}, fmt.Errorf("%w: empty or null", ErrInvalidCriticResult)
	}
	var doc CriticResult
	if err := json.Unmarshal(trimmed, &doc); err != nil {
		return CriticResult{}, fmt.Errorf("%w: %v", ErrInvalidCriticResult, err)
	}
	doc.Score = clampScore(doc.Score)
	return doc, nil
}

// ShouldRefine 判断在给定自评下是否还应继续细化一轮。
//
// 仅当钳制后的 Score 严格小于 threshold，且已用轮次严格小于上限时返回 true；
// maxIterations <= 0 时回退到 DefaultMaxRefinementIterations。
// 本函数是纯函数，不读取任何全局状态，也不修改入参。
func ShouldRefine(r CriticResult, threshold float64, iterations, maxIterations int) bool {
	if maxIterations <= 0 {
		maxIterations = DefaultMaxRefinementIterations
	}
	if iterations >= maxIterations {
		return false
	}
	return clampScore(r.Score) < threshold
}

// clampScore 把分数钳制到 [0, 1]：负值为 0，超过 1 为 1，NaN 归零（±Inf 自然落到边界）。
func clampScore(score float64) float64 {
	switch {
	case math.IsNaN(score), score < 0:
		return 0
	case score > 1:
		return 1
	default:
		return score
	}
}
