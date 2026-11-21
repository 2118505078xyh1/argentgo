package eval

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// 本文件为 ratchet 爬山状态提供序列化原语，供以后把 BestScore/History
// 落盘或跨进程传递使用。所有函数都是纯函数：不读时钟、网络或全局可变
// 状态，不修改入参，也不触碰现有 golden/评分路径。目前没有调用方，
// 不改变 Decide 的行为。
//
// 序列化采用显式的 wire 结构而非直接编码 RatchetState，这样可以锁定
// 字段顺序与命名，避免日后重排字段导致字节快照漂移。编码时关闭 HTML
// 转义，使中文 reason 原样落盘而不是变成 \uXXXX。

// ErrInvalidRatchetJSON 是 ratchet 状态反序列化失败时返回的哨兵错误，
// 调用方可用 errors.Is 匹配。空输入、非对象输入与畸形 JSON 都会包裹它。
var ErrInvalidRatchetJSON = errors.New("eval: invalid ratchet JSON state")

// ratchetEntryWire 是 RatchetEntry 的显式 JSON 表示。字段声明顺序即输出顺序，
// json tag 锁定键名，保证跨版本的稳定字节。
type ratchetEntryWire struct {
	TrainMeanDiff float64 `json:"train_mean_diff"`
	HoldoutCILow  float64 `json:"holdout_ci_low"`
	Epsilon       float64 `json:"epsilon"`
	PrevBestScore float64 `json:"prev_best_score"`
	BestScore     float64 `json:"best_score"`
	Accept        bool    `json:"accept"`
	Reason        string  `json:"reason"`
}

// ratchetStateWire 是 RatchetState 的显式 JSON 表示。字段声明顺序即输出顺序。
type ratchetStateWire struct {
	BestScore float64            `json:"best_score"`
	History   []ratchetEntryWire `json:"history"`
}

// RatchetToJSON 把 ratchet 状态编码为稳定的 JSON 字节。
//
// 稳定性体现在三点：
//   - 字段顺序由 ratchetStateWire/ratchetEntryWire 的声明顺序固定；
//   - SetEscapeHTML(false) 让中文与 <>& 原样输出，不产生 \uXXXX 转义；
//   - 去掉 json.Encoder 额外追加的尾部换行，输出不带尾随换行。
//
// RatchetFromJSON(RatchetToJSON(s)) 与 s 等价（含 nil 与空 History 的区分），
// 因此可安全 round-trip。若状态含 NaN/Inf 等无法编码的浮点数，返回错误。
func RatchetToJSON(s RatchetState) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(ratchetWireFromState(s)); err != nil {
		return nil, fmt.Errorf("ratchet: encode state: %w", err)
	}

	out := buf.Bytes()
	// json.Encoder 总会追加一个换行；稳定字节要求把它去掉。
	if n := len(out); n > 0 && out[n-1] == '\n' {
		out = out[:n-1]
	}
	return out, nil
}

// RatchetFromJSON 从 JSON 字节还原 ratchet 状态。
//
// 空输入、空白输入、顶层非对象（如 null/数组/标量）以及畸形 JSON 都返回
// 包裹 ErrInvalidRatchetJSON 的错误，调用方可用 errors.Is 匹配。
// nil History（JSON 中为 null）与空 History（JSON 中为 []）会被区分保留，
// 从而与 RatchetToJSON 构成安全 round-trip。
func RatchetFromJSON(data []byte) (RatchetState, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return RatchetState{}, fmt.Errorf("%w: empty input", ErrInvalidRatchetJSON)
	}
	if trimmed[0] != '{' {
		return RatchetState{}, fmt.Errorf("%w: expected a JSON object", ErrInvalidRatchetJSON)
	}

	var w ratchetStateWire
	if err := json.Unmarshal(data, &w); err != nil {
		return RatchetState{}, fmt.Errorf("%w: %v", ErrInvalidRatchetJSON, err)
	}
	return ratchetStateFromWire(w), nil
}

// MergeRatchet 合并两个 ratchet 状态：BestScore 取两者较大值，History 按
// ratchetEntryKey 这一稳定键去重后按 a 在前、b 在后的顺序拼接。
//
// 这是一个纯函数：不修改 a/b，返回的 History 是全新切片，与入参不共享
// 底层数组。重复条目只保留第一次出现的位置，因此合并结果只依赖入参内容，
// 与调用次数无关。
func MergeRatchet(a, b RatchetState) RatchetState {
	best := a.BestScore
	if b.BestScore > best {
		best = b.BestScore
	}

	merged := RatchetState{BestScore: best}
	// 只要任一入参的 History 非 nil，结果就保持非 nil（哪怕最终没有任何条目），
	// 与序列化层「nil 与空数组区分」的语义保持一致。
	if a.History != nil || b.History != nil {
		merged.History = make([]RatchetEntry, 0, len(a.History)+len(b.History))
	}
	seen := make(map[string]struct{}, len(a.History)+len(b.History))
	for _, src := range [][]RatchetEntry{a.History, b.History} {
		for _, e := range src {
			key := ratchetEntryKey(e)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			merged.History = append(merged.History, e)
		}
	}
	return merged
}

// ratchetEntryKey 为一个历史条目生成稳定且可区分的去重键。
// 浮点字段用 %.17g 编码以保留完整精度，Reason 原样拼入以区分不同决策理由。
func ratchetEntryKey(e RatchetEntry) string {
	return fmt.Sprintf("%.17g|%.17g|%.17g|%.17g|%.17g|%t|%s",
		e.TrainMeanDiff, e.HoldoutCILow, e.Epsilon,
		e.PrevBestScore, e.BestScore, e.Accept, e.Reason)
}

// ratchetWireFromState 把状态转成显式 wire 结构，并复制 History。
func ratchetWireFromState(s RatchetState) ratchetStateWire {
	w := ratchetStateWire{BestScore: s.BestScore}
	if s.History != nil {
		w.History = make([]ratchetEntryWire, len(s.History))
		for i, e := range s.History {
			w.History[i] = ratchetEntryWire{
				TrainMeanDiff: e.TrainMeanDiff,
				HoldoutCILow:  e.HoldoutCILow,
				Epsilon:       e.Epsilon,
				PrevBestScore: e.PrevBestScore,
				BestScore:     e.BestScore,
				Accept:        e.Accept,
				Reason:        e.Reason,
			}
		}
	}
	return w
}

// ratchetStateFromWire 把 wire 结构转回状态，并复制 History 以隔离底层数组。
func ratchetStateFromWire(w ratchetStateWire) RatchetState {
	s := RatchetState{BestScore: w.BestScore}
	if w.History != nil {
		s.History = make([]RatchetEntry, len(w.History))
		for i, e := range w.History {
			s.History[i] = RatchetEntry{
				TrainMeanDiff: e.TrainMeanDiff,
				HoldoutCILow:  e.HoldoutCILow,
				Epsilon:       e.Epsilon,
				PrevBestScore: e.PrevBestScore,
				BestScore:     e.BestScore,
				Accept:        e.Accept,
				Reason:        e.Reason,
			}
		}
	}
	return s
}
