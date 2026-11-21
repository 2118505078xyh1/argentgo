package eval

import (
	"fmt"
	"sort"
	"strings"
)

// 本文件把「A/B 两个变体臂的评分结果」按场景指纹配对，再接到已有的
// CompareResults / PairedDelta 原语上，产出稳定的中文 verdict 与配对明细。
//
// 它是 R39 研究缺口的闭环接线点：ScenarioFingerprint 负责给两臂场景生成可
// 跨臂匹配的身份，CompareResults（内部复用 PairedDelta / ClassifyDelta /
// FormatDeltaReport / BuildCompareDiagnostic）负责统计与结论。这里不重造统计，
// 只做「按指纹对齐场景」这件 compare.go 不做的事——compare.go 要求两份向量
// 已经按场景一一对应，而真实 A/B 两臂的顺序、命名甚至数量都可能不同。
//
// 与同目录其余原语一致：纯函数，不修改入参，不读时钟/网络/文件/全局可变状态，
// 不触碰 golden/评分路径。

// variantArmBaseline / variantArmCandidate 是配对明细里标记来源臂的稳定文本。
const (
	variantArmBaseline  = "baseline"
	variantArmCandidate = "candidate"
)

// 未配对原因的稳定中文标记。
const (
	variantReasonMissingFP     = "指纹缺失"  // 该场景在所属臂的指纹映射中查不到
	variantReasonNoCounterpart = "无对应指纹" // 指纹存在，但另一臂没有同指纹的场景
)

// VariantABPair 是一个成功配对的场景：两臂通过相同指纹对应到同一个场景身份。
// 字段都是值类型，测试可直接断言，无需解析渲染文本。
type VariantABPair struct {
	Fingerprint     string // 两臂共用的场景指纹
	BaselineName    string // arm A（基线）的场景名
	CandidateName   string // arm B（候选）的场景名
	BaselinePassed  bool
	CandidatePassed bool
}

// VariantABUnmatched 是一个未能配对的场景。
type VariantABUnmatched struct {
	Arm         string // variantArmBaseline 或 variantArmCandidate
	Name        string
	Fingerprint string // 指纹缺失时为空串
	Reason      string // variantReasonMissingFP 或 variantReasonNoCounterpart
}

// VariantABReport 是 RunVariantAB 的结构化结果。
type VariantABReport struct {
	Pairs          []VariantABPair      // 成功配对，按指纹（再按名字）排序，保证确定性
	Unmatched      []VariantABUnmatched // 未配对，按 (Arm, Name, Reason) 排序
	PairedCount    int                  // len(Pairs)，成功配对的场景数
	VerdictSummary string               // 稳定中文结论：显著提升/显著退化/不显著/样本不足/无有效配对
	Text           string               // 完整中文报告：CompareResults 文本 + 配对明细段
}

// FingerprintsFromScenarios 用 ScenarioFingerprint 给一组场景建立「场景名 → 指纹」映射，
// 供 RunVariantAB 使用。两臂即便对 prompt 做了不同变体改写，只要用同一份原始场景
// （或同一套规范名/原文）生成指纹，就能跨臂稳定配对。
//
// 纯函数：返回新映射，不修改入参；同名场景以最后一次出现为准。
func FingerprintsFromScenarios(scenarios []Scenario) map[string]string {
	m := make(map[string]string, len(scenarios))
	for _, s := range scenarios {
		m[s.Name] = ScenarioFingerprint(s.Name, s.Prompt)
	}
	return m
}

// fingerprintOf 返回场景在给定指纹映射中的指纹。
// 映射为 nil 时退化为「按场景名配对」：名字即身份键。这是 CLI 未提供指纹文件
// 时的带外默认路径，使 ab 前缀在没有指纹产物时依然可用。
func fingerprintOf(name string, m map[string]string) (string, bool) {
	if m == nil {
		return name, true
	}
	fp, ok := m[name]
	return fp, ok
}

// armIndex 把一臂的 Result 向量按指纹分组。查不到指纹的场景记入 unmatched。
// 组内按场景名稳定排序，避免输入顺序影响最终配对结果。
func armIndex(results []Result, arm string, m map[string]string) (
	byFP map[string][]Result, unmatched []VariantABUnmatched,
) {
	byFP = make(map[string][]Result)
	type keyed struct {
		result Result
		fp     string
	}
	var all []keyed
	for _, r := range results {
		fp, ok := fingerprintOf(r.Name, m)
		if !ok {
			unmatched = append(unmatched, VariantABUnmatched{
				Arm: arm, Name: r.Name, Reason: variantReasonMissingFP,
			})
			continue
		}
		all = append(all, keyed{result: r, fp: fp})
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].fp != all[j].fp {
			return all[i].fp < all[j].fp
		}
		return all[i].result.Name < all[j].result.Name
	})
	for _, k := range all {
		byFP[k.fp] = append(byFP[k.fp], k.result)
	}
	return byFP, unmatched
}

// RunVariantAB 按场景指纹把两臂 Result 向量配对，并复用 CompareResults 出中文 verdict。
//
// fingerprintsA / fingerprintsB 分别是 arm A（基线）与 arm B（候选）的「场景名 → 指纹」
// 映射，通常用 FingerprintsFromScenarios 生成。任一映射为 nil 时，该臂退化为按场景名
// 配对；非 nil 但查不到某个场景名时，该场景记为「指纹缺失」并排除出配对。
//
// 配对规则：
//   - 指纹相同即视为同一场景（与名字、顺序无关）；
//   - 同指纹下两臂按名字排序后逐位配对；某臂多出的场景记为「无对应指纹」；
//   - Pairs 与 Unmatched 都排序，输出确定，与入参遍历顺序无关。
//
// 配对出的两臂向量交给 CompareResults：样本不足守卫、0/1 翻转诊断与显著性结论
// 全部由既有原语给出，本函数不重造统计。n=0 时没有可比较的向量，直接给出
// 「无有效配对」结论并照常输出配对明细；n>=1 时 CompareResults 自身能处理 n<2。
func RunVariantAB(baseline, candidate []Result, fingerprintsA, fingerprintsB map[string]string) VariantABReport {
	byA, unmatchedA := armIndex(baseline, variantArmBaseline, fingerprintsA)
	byB, unmatchedB := armIndex(candidate, variantArmCandidate, fingerprintsB)

	// 遍历两臂指纹的并集，升序保证确定性。只有两臂都出现的指纹才配对；
	// 仅在一臂出现的指纹（以及同指纹下多出的场景）都记为「无对应指纹」。
	fps := make([]string, 0, len(byA)+len(byB))
	seen := make(map[string]bool, len(byA)+len(byB))
	for fp := range byA {
		seen[fp] = true
	}
	for fp := range byB {
		seen[fp] = true
	}
	for fp := range seen {
		fps = append(fps, fp)
	}
	sort.Strings(fps)

	pairs := make([]VariantABPair, 0, len(baseline))
	for _, fp := range fps {
		a, b := byA[fp], byB[fp]
		n := len(a)
		if len(b) < n {
			n = len(b)
		}
		for i := 0; i < n; i++ {
			pairs = append(pairs, VariantABPair{
				Fingerprint:     fp,
				BaselineName:    a[i].Name,
				CandidateName:   b[i].Name,
				BaselinePassed:  a[i].Passed,
				CandidatePassed: b[i].Passed,
			})
		}
		// 仅在一臂出现、或同指纹下多出的场景：另一臂没有可配对的对手。
		for _, r := range a[n:] {
			unmatchedA = append(unmatchedA, VariantABUnmatched{
				Arm: variantArmBaseline, Name: r.Name, Fingerprint: fp,
				Reason: variantReasonNoCounterpart,
			})
		}
		for _, r := range b[n:] {
			unmatchedB = append(unmatchedB, VariantABUnmatched{
				Arm: variantArmCandidate, Name: r.Name, Fingerprint: fp,
				Reason: variantReasonNoCounterpart,
			})
		}
	}

	sort.SliceStable(unmatchedA, func(i, j int) bool {
		if unmatchedA[i].Name != unmatchedA[j].Name {
			return unmatchedA[i].Name < unmatchedA[j].Name
		}
		return unmatchedA[i].Reason < unmatchedA[j].Reason
	})
	sort.SliceStable(unmatchedB, func(i, j int) bool {
		if unmatchedB[i].Name != unmatchedB[j].Name {
			return unmatchedB[i].Name < unmatchedB[j].Name
		}
		return unmatchedB[i].Reason < unmatchedB[j].Reason
	})
	unmatched := make([]VariantABUnmatched, 0, len(unmatchedA)+len(unmatchedB))
	unmatched = append(unmatched, unmatchedA...)
	unmatched = append(unmatched, unmatchedB...)

	report := VariantABReport{
		Pairs:       pairs,
		Unmatched:   unmatched,
		PairedCount: len(pairs),
	}

	if report.PairedCount == 0 {
		report.VerdictSummary = "无有效配对"
		report.Text = "verdict: 无有效配对（没有同指纹的 A/B 场景），无法判定变体差异\n"
		report.Text += formatVariantPairDetail(report)
		return report
	}

	// 构造已按场景对齐的两臂向量，交给 compare.go 的既有统计路径。
	alignedA := make([]Result, 0, report.PairedCount)
	alignedB := make([]Result, 0, report.PairedCount)
	for _, p := range pairs {
		alignedA = append(alignedA, Result{Name: p.BaselineName, Passed: p.BaselinePassed})
		alignedB = append(alignedB, Result{Name: p.CandidateName, Passed: p.CandidatePassed})
	}
	comparison, err := CompareResults(alignedA, alignedB)
	if err != nil {
		// CompareResults 在 n>=1 时不应报错；万一报错也不向外抛，
		// 而是把失败显式写进报告并标为「无法判定」。
		report.VerdictSummary = "无法判定"
		report.Text = fmt.Sprintf("verdict: 无法判定（配对统计失败：%v）\n", err)
		report.Text += formatVariantPairDetail(report)
		return report
	}
	report.VerdictSummary = summarizeVariantVerdict(comparison)
	report.Text = comparison + formatVariantPairDetail(report)
	return report
}

// summarizeVariantVerdict 从 CompareResults 的稳定 verdict 行提取简短中文结论，
// 避免在 RunVariantAB 里重复一遍显著性分类逻辑。
func summarizeVariantVerdict(report string) string {
	for _, line := range strings.Split(report, "\n") {
		switch {
		case strings.HasPrefix(line, "verdict: 显著提升"):
			return "显著提升"
		case strings.HasPrefix(line, "verdict: 显著退化"):
			return "显著退化"
		case strings.HasPrefix(line, "verdict: 样本不足"):
			return "样本不足"
		case strings.HasPrefix(line, "verdict: 差异不显著"):
			return "不显著"
		}
	}
	return "无法判定"
}

// variantPassLabel 把 Passed 映射为稳定的中文标记。
func variantPassLabel(passed bool) string {
	if passed {
		return "通过"
	}
	return "失败"
}

// formatVariantPairDetail 渲染配对明细段：配对数、逐对指纹与两臂场景、未配对原因。
// 文本只依赖入参，格式固定，适合快照比对。
func formatVariantPairDetail(r VariantABReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "配对明细:\n")
	fmt.Fprintf(&b, "  配对数 n=%d\n", r.PairedCount)
	if len(r.Pairs) == 0 {
		fmt.Fprintf(&b, "  配对: 无\n")
	}
	for i, p := range r.Pairs {
		fmt.Fprintf(&b, "  %d. 指纹=%s A=%q(%s) ↔ B=%q(%s)\n",
			i+1, p.Fingerprint, p.BaselineName, variantPassLabel(p.BaselinePassed),
			p.CandidateName, variantPassLabel(p.CandidatePassed))
	}
	if len(r.Unmatched) == 0 {
		fmt.Fprintf(&b, "  未配对: 无\n")
	} else {
		fmt.Fprintf(&b, "  未配对 %d 项:\n", len(r.Unmatched))
		for _, u := range r.Unmatched {
			fmt.Fprintf(&b, "    - 臂=%s 场景=%q 原因=%s", u.Arm, u.Name, u.Reason)
			if u.Fingerprint != "" {
				fmt.Fprintf(&b, " 指纹=%s", u.Fingerprint)
			}
			fmt.Fprintf(&b, "\n")
		}
	}
	return b.String()
}
