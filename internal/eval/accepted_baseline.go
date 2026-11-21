package eval

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// 本文件实现「已接受基线指纹账本」。此前 CompareResults 每次都要手工传入两份
// 结果文件，ratchet 的 BestScore/History 又只活在内存里，序列化原语没有调用方，
// 因此改动之后无从判定「本次运行是否相对已接受基线退化」。这里提供一条纯函数
// 路径：把一组评分结果固化为一条带指纹与分数的基线记录，再用候选结果与基线
// 分数比较，给出 accepted / regressed / insufficient 三态判定。
//
// 所有函数都是纯函数：不读时钟/网络/文件/全局可变状态，不修改入参，也不接线到
// 核心 agent 循环或现有 golden 评分路径。序列化沿用 ratchet_persist.go 的做法：
// 显式 wire 结构锁定字段顺序，关闭 HTML 转义使中文 note 原样落盘。

// minBaselineSamples 是给出回归判定所需的最小候选样本数。样本少于 2 时，
// 完成率只是一个二进制信号的比例，差异不具统计意义，一律降级为样本不足。
const minBaselineSamples = 2

// baselineScoreEpsilon 是完成率比较的浮点容差，避免诸如 2/3 与 0.6666… 的
// 浮点表示噪声把「相等」误判为「退化」。
const baselineScoreEpsilon = 1e-9

// ErrInvalidBaselineJSON 是已接受基线反序列化失败时返回的哨兵错误，
// 调用方可用 errors.Is 匹配。空输入、非对象输入与畸形 JSON 都会包裹它。
var ErrInvalidBaselineJSON = errors.New("eval: invalid accepted baseline JSON")

// RegressionVerdict 是候选结果相对已接受基线的判定枚举。
// 底层为 string，便于直接落盘与在报告中展示。
type RegressionVerdict string

const (
	// RegressionAccepted 表示候选完成率不低于基线分数（没有退化）。
	RegressionAccepted RegressionVerdict = "accepted"
	// RegressionRegressed 表示候选完成率低于基线分数（发生退化）。
	RegressionRegressed RegressionVerdict = "regressed"
	// RegressionInsufficient 表示样本不足，无法给出可靠的回归结论。
	RegressionInsufficient RegressionVerdict = "insufficient"
)

// AcceptedBaseline 是一条已接受基线记录，构成指纹账本的一行。
//
//   - Fingerprint 是场景身份指纹，由结果的 index+name 序列得出，用于辨认这条
//     基线覆盖的是哪一组场景（与 ScenarioFingerprint 同样的长度前缀编码）。
//   - Score 是基线运行的完成率（通过数 / 总数，[0,1]）。
//   - ResultsHash 是整条结果向量的稳定哈希，锁定 index/name/passed/reason/
//     stopped/turns；token 与耗时等诊断字段不进入哈希，避免无关抖动改变身份。
//   - Note 是人类可读备注（允许中文），随记录一起序列化。
type AcceptedBaseline struct {
	Fingerprint string  `json:"fingerprint"`
	Score       float64 `json:"score"`
	ResultsHash string  `json:"results_hash"`
	Note        string  `json:"note"`
}

// BaselineFromResults 把一组评分结果固化为一条已接受基线。
//
// Score 取完成率；Fingerprint 取场景身份指纹；ResultsHash 取整条结果向量的
// SHA-256。该函数是纯函数：只读 results，不修改任何入参字段或切片；
// 同样的 (results, note) 永远得到同样的记录。note 原样保存，不做裁剪。
func BaselineFromResults(results []Result, note string) AcceptedBaseline {
	return AcceptedBaseline{
		Fingerprint: baselineScenarioFingerprint(results),
		Score:       baselineCompletionRate(results),
		ResultsHash: baselineResultsHash(results),
		Note:        note,
	}
}

// baselineCompletionRate 返回结果向量的完成率；空向量按 0 处理。
func baselineCompletionRate(results []Result) float64 {
	if len(results) == 0 {
		return 0
	}
	passed := 0
	for _, r := range results {
		if r.Passed {
			passed++
		}
	}
	return float64(passed) / float64(len(results))
}

// baselineScenarioFingerprint 由结果的 index+name 序列得出场景身份指纹，
// 编码方式与 ScenarioFingerprint 一致（十进制长度前缀消除字段边界歧义）。
func baselineScenarioFingerprint(results []Result) string {
	h := sha256.New()
	fmt.Fprintf(h, "scenarios:%d|", len(results))
	for _, r := range results {
		fmt.Fprintf(h, "%d|", r.Index)
		writeBaselineField(h, r.Name)
		fmt.Fprint(h, "|")
	}
	return hex.EncodeToString(h.Sum(nil))
}

// baselineResultsHash 对整条结果向量取稳定 SHA-256。只纳入定义「这次运行
// 接受了什么」的字段；Usage/Duration/Tools/Text 是诊断信息，不参与身份。
func baselineResultsHash(results []Result) string {
	h := sha256.New()
	fmt.Fprintf(h, "results:%d|", len(results))
	for _, r := range results {
		fmt.Fprintf(h, "%d|", r.Index)
		writeBaselineField(h, r.Name)
		fmt.Fprintf(h, "|%t|", r.Passed)
		writeBaselineField(h, r.Reason)
		fmt.Fprintf(h, "|%t|%d|", r.Stopped, r.Turns)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// writeBaselineField 以「十进制长度:内容」写入一个字段，消除字段边界歧义。
func writeBaselineField(w io.Writer, s string) {
	fmt.Fprintf(w, "%d:%s", len(s), s)
}

// CheckRegression 判定候选结果是否相对已接受基线退化。
//
// 规则（按优先级）：
//  1. len(candidate) < minBaselineSamples：返回 RegressionInsufficient，
//     不给出「提升/退化」结论；
//  2. 候选完成率 < 基线 Score - baselineScoreEpsilon：返回 RegressionRegressed；
//  3. 其余：返回 RegressionAccepted（完成率持平或更高）。
//
// 该函数是纯函数：只读 baseline 与 candidate，不修改入参，也不触碰任何
// 全局状态。注意它只依据样本充足性与完成率比较，不因为 Fingerprint 不一致
// 而拒绝判定——账本指纹供调用方核对场景集合是否可比，判定本身保持简单确定。
func CheckRegression(baseline AcceptedBaseline, candidate []Result) RegressionVerdict {
	if len(candidate) < minBaselineSamples {
		return RegressionInsufficient
	}
	rate := baselineCompletionRate(candidate)
	if rate < baseline.Score-baselineScoreEpsilon {
		return RegressionRegressed
	}
	return RegressionAccepted
}

// acceptedBaselineWire 是 AcceptedBaseline 的显式 JSON 表示。字段声明顺序
// 即输出顺序，json tag 锁定键名，保证跨版本字节稳定。
type acceptedBaselineWire struct {
	Fingerprint string  `json:"fingerprint"`
	Score       float64 `json:"score"`
	ResultsHash string  `json:"results_hash"`
	Note        string  `json:"note"`
}

// BaselineToJSON 把已接受基线编码为稳定的 JSON 字节。
//
// SetEscapeHTML(false) 让中文与 <>& 原样输出，不产生 \uXXXX 转义；
// 同时去掉 json.Encoder 附加的尾随换行，使输出不带尾部换行。
// 若 Score 为 NaN/Inf 等无法编码的浮点数，返回包装后的错误。
func BaselineToJSON(b AcceptedBaseline) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(acceptedBaselineWire{
		Fingerprint: b.Fingerprint,
		Score:       b.Score,
		ResultsHash: b.ResultsHash,
		Note:        b.Note,
	}); err != nil {
		return nil, fmt.Errorf("eval: encode accepted baseline: %w", err)
	}

	out := buf.Bytes()
	if n := len(out); n > 0 && out[n-1] == '\n' {
		out = out[:n-1]
	}
	return out, nil
}

// BaselineFromJSON 从 JSON 字节还原已接受基线。
//
// 空输入、空白输入、顶层非对象（如 null/数组/标量）以及畸形 JSON 都返回
// 包裹 ErrInvalidBaselineJSON 的错误，调用方可用 errors.Is 匹配。
// BaselineFromJSON(BaselineToJSON(b)) 与 b 等价，可安全 round-trip。
func BaselineFromJSON(data []byte) (AcceptedBaseline, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return AcceptedBaseline{}, fmt.Errorf("%w: empty input", ErrInvalidBaselineJSON)
	}
	if trimmed[0] != '{' {
		return AcceptedBaseline{}, fmt.Errorf("%w: expected a JSON object", ErrInvalidBaselineJSON)
	}

	var w acceptedBaselineWire
	if err := json.Unmarshal(data, &w); err != nil {
		return AcceptedBaseline{}, fmt.Errorf("%w: %v", ErrInvalidBaselineJSON, err)
	}
	return AcceptedBaseline{
		Fingerprint: w.Fingerprint,
		Score:       w.Score,
		ResultsHash: w.ResultsHash,
		Note:        w.Note,
	}, nil
}
