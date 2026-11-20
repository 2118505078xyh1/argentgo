// eval 的 edit 基准端到端测量真实的内置编辑工具，针对模型形态的编辑场景。
// 端到端针对模型形态的编辑场景。
//
// Methodology
//
// 每个用例声明模型看到的字节（Original）、它会发送的
// 每个用例声明模型看到的字节（Original）、它会发送的参数（Old/New/Anchor/AnchorHash/Repair/Hashline）以及期望结果。
// RunEditCase 在临时工作区下种入临时文件，构建工具 schema 接受的 JSON
// 参数对象，并调用 tools.Registry.Run，名称为 "edit"。这完整驱动已发布的代码路径：参数解码
// 名称为 "edit"。这完整驱动已发布的代码路径：参数解码
// 及 old/old_text 别名、工作区路径解析、变更锁、
// 默认/repair/hashline 解析分支，以及最终的
// 默认/repair/hashline 解析分支，以及最终的 os.WriteFile。结果文件字节和返回的错误被
// 对照用例声明的期望评分。
//
// 对照用例声明的期望评分。
// 这刻意不是解析辅助函数的重新实现。基准的早期版本逐行镜像 internal/tools 的未导出辅助函数；
// 逐行；它只能证明镜像与自身一致，其
// 它只能证明镜像与自身一致，其报告诚实地标注"100% 是回归守卫，不是真实工具路径"。
// 下面的 registry.Run 路径测量用户实际执行的代码。
//
// 它不测量的是：agent 循环、模型/网络调用，或真实的
// 它不测量的是：agent 循环、模型/网络调用，或真实的策略/审批接线。harness 构造 yolo registry 使
// 修改类工具得以运行；一个测试用 yolo=false 的变更门作为
// 签名，证明使用的是 registry 路径。它完全离线且
// deterministic.
//
// 用例"通过"是指观察到的结果匹配用例声明的
// 期望：WantErrContains 断言错误文本包含 needle；
// WantResult 断言成功编辑且最终内容包含 needle；
// 两者都没有时，任何成功编辑都通过。期望失败的用例与期望成功的用例同样
// 重要：一个总是报告通过的 harness 毫无价值。
// 毫无价值。

package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/2118505078xyh1/argentgo/internal/tools"
)

// 基准理解的编辑模式。
const (
	modeExact    = "exact"
	modeRepair   = "repair"
	modeHashline = "hashline"
)

// editBenchFile 是每个用例应用到的临时文件。它以相对路径传给
// 工具，因此工具的诊断原样命名它，且 fixture
// 的错误 needle 保持稳定。
const editBenchFile = "edit.txt"

// 工具，因此工具的诊断原样命名它，且 fixture 的错误 needle 保持稳定。
// EditCase 是一个脚本化编辑场景。Original 是模型看到的文件内容；Anchor 是它在 hashline 模式下哈希的精确文本；AnchorHash 是
// 模型提供的哈希（空表示"不发送 anchor_hash"，使
// 模型提供的哈希（空表示"不发送 anchor_hash"，使真实工具按内容锚定）；ShiftLines 是在匹配目标前插入的文本，
// 用于模拟读与写之间在其上方添加的行。
type EditCase struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Original        string `json:"original"`
	Anchor          string `json:"anchor,omitempty"`
	Old             string `json:"old,omitempty"`
	New             string `json:"new"`
	Mode            string `json:"mode"`
	AnchorHash      string `json:"anchor_hash,omitempty"`
	ShiftLines      string `json:"shift_lines,omitempty"`
	WantResult      string `json:"want_result,omitempty"`
	WantErrContains string `json:"want_err_contains,omitempty"`
}

// ModeStat 聚合某一种编辑模式的结果。Passed/Failed 是权威门；下面的 ES/EM 字段仅作诊断。
// 权威门；下面的 ES/EM 字段仅作诊断。
type ModeStat struct {
	Passed         int     `json:"passed"`
	Failed         int     `json:"failed"`
	ExactMatches   int     `json:"exact_matches,omitempty"`
	MeanSimilarity float64 `json:"mean_similarity,omitempty"`

	// similaritySum/similarityN 是 MeanSimilarity 的运行局部累加器；
	// 它们刻意不导出且从不进入 JSON 报告。
	similaritySum float64
	similarityN   int
}

// EditCaseResult 是用例的评分结果。Passed 表示真实工具
// 匹配了用例声明的期望（其本身可能是错误）。
type EditCaseResult struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Mode   string `json:"mode"`
	Passed bool   `json:"passed"`
	Got    string `json:"got,omitempty"`
	Reason string `json:"reason,omitempty"`

	// 匹配了用例声明的期望（其本身可能是错误）。
	// Similarity 是产生内容与 WantResult 之间的诊断性编辑相似度，仅为声明了 WantResult 且编辑成功的
	// 且编辑成功的期望成功用例。当任一输入超过 rune 上限时为 -1
	// 期望成功用例计算。当任一输入超过 rune 上限时为 -1（见 Levenshtein）；调用方将其标为"unbounded"。Similarity
	// 从不被通过/失败门参考。
	Similarity float64 `json:"similarity,omitempty"`

	// scored/tokenExact 是报告聚合的运行局部辅助函数。
	scored     bool
	tokenExact bool
}

// EditBenchReport 汇总整个基准运行。
type EditBenchReport struct {
	Total    int                 `json:"total"`
	Passed   int                 `json:"passed"`
	Failed   int                 `json:"failed"`
	ByMode   map[string]ModeStat `json:"by_mode"`
	Failures []EditCaseResult    `json:"failures,omitempty"`

	// MeanSimilarity、ExactMatches 和 Scored 是相似度评分用例的诊断性聚合。
	// 相似度评分用例。它们从不影响权威通过率。
	MeanSimilarity float64 `json:"mean_similarity,omitempty"`
	ExactMatches   int     `json:"exact_matches,omitempty"`
	Scored         int     `json:"scored,omitempty"`
}

// 它们从不影响权威通过率。
// LoadEditCases 读取 JSON fixture：裸用例数组或 {"cases": [...]}。容忍 UTF-8 BOM，与 eval.Load 一致。
func LoadEditCases(path string) ([]EditCase, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cases, err := ParseEditCases(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cases, nil
}

// ParseEditCases 解码并验证 JSON 编辑用例文档。
func ParseEditCases(data []byte) ([]EditCase, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	var document struct {
		Cases []EditCase `json:"cases"`
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		if err := json.Unmarshal(data, &document.Cases); err != nil {
			return nil, fmt.Errorf("decode edit cases: %w", err)
		}
	} else if err := json.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("decode edit cases: %w", err)
	}
	if len(document.Cases) == 0 {
		return nil, fmt.Errorf("no edit cases in document")
	}
	seen := make(map[string]bool, len(document.Cases))
	for i := range document.Cases {
		c := &document.Cases[i]
		if c.ID == "" {
			c.ID = fmt.Sprintf("case-%d", i+1)
		}
		if c.Name == "" {
			c.Name = c.ID
		}
		if c.Mode == "" {
			c.Mode = modeExact
		}
		if c.Mode != modeExact && c.Mode != modeRepair && c.Mode != modeHashline {
			return nil, fmt.Errorf("case %d (%s): invalid mode %q", i+1, c.ID, c.Mode)
		}
		if seen[c.ID] {
			return nil, fmt.Errorf("case %d: duplicate id %q", i+1, c.ID)
		}
		seen[c.ID] = true
		if c.WantResult == "" && c.WantErrContains == "" {
			return nil, fmt.Errorf("case %d (%s): no expectation declared", i+1, c.ID)
		}
	}
	return document.Cases, nil
}

// editToolArgs 是编辑工具的 JSON schema。omitempty 使未声明的
// 字段不进入线上，因此 harness 恰好发送 fixture 描述的
// 参数；模式字段由用例的模式派生。
type editToolArgs struct {
	Path       string `json:"path"`
	Old        string `json:"old,omitempty"`
	New        string `json:"new,omitempty"`
	Repair     bool   `json:"repair,omitempty"`
	Hashline   bool   `json:"hashline,omitempty"`
	Anchor     string `json:"anchor,omitempty"`
	AnchorHash string `json:"anchor_hash,omitempty"`
}

// editArgs 将用例渲染为已发布编辑工具期望的参数。
func editArgs(c EditCase) (string, error) {
	args := editToolArgs{
		Path:       editBenchFile,
		Old:        c.Old,
		New:        c.New,
		Anchor:     c.Anchor,
		AnchorHash: c.AnchorHash,
	}
	switch c.Mode {
	case modeRepair:
		args.Repair = true
	case modeHashline:
		args.Hashline = true
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return "", fmt.Errorf("encode edit arguments: %w", err)
	}
	return string(raw), nil
}

// seedEditFile 将工具应看到的字节写入临时工作区。
func seedEditFile(root string, c EditCase) (string, error) {
	path := filepath.Join(root, editBenchFile)
	if err := os.WriteFile(path, []byte(buildCurrentContent(c)), 0o644); err != nil {
		return "", fmt.Errorf("cannot seed scratch file: %w", err)
	}
	return path, nil
}

// runEditViaRegistry 通过公开 registry 调用已发布的编辑工具。
// yolo=true 时修改类工具运行；false 时 registry 在工具被到达前拒绝它（测试用作真实路径签名）。
// 在工具被到达前拒绝它（测试用作真实路径签名）。
func runEditViaRegistry(root string, c EditCase, yolo bool) (string, error) {
	args, err := editArgs(c)
	if err != nil {
		return "", err
	}
	reg := tools.New(root, yolo)
	return reg.Run(context.Background(), "edit", args)
}

// RunEditCase 通过真实编辑工具应用一个用例并评分结果。
// 所有 I/O 都限制在 MkdirTemp 临时目录中，该目录在之前被移除
// returning.
func RunEditCase(c EditCase) EditCaseResult {
	mode := c.Mode
	if mode == "" {
		mode = modeExact
	}
	res := EditCaseResult{ID: c.ID, Name: c.Name, Mode: mode}
	if mode != modeExact && mode != modeRepair && mode != modeHashline {
		res.Reason = "unknown mode " + strconv.Quote(mode)
		return res
	}

	dir, err := os.MkdirTemp("", "editbench-")
	if err != nil {
		res.Reason = "cannot create scratch directory: " + err.Error()
		return res
	}
	defer os.RemoveAll(dir)

	path, err := seedEditFile(dir, c)
	if err != nil {
		res.Reason = err.Error()
		return res
	}

	_, applyErr := runEditViaRegistry(dir, c, true)
	final := ""
	if applyErr == nil {
		edited, err := os.ReadFile(path)
		if err != nil {
			res.Reason = "cannot read edited file: " + err.Error()
			return res
		}
		final = string(edited)
	}

	passed, got, reason := gradeEditCase(c, final, applyErr)
	res.Passed, res.Got, res.Reason = passed, got, reason

	// 仅诊断：对期望成功的用例比较产生的字节与声明的目标。
	// 仅诊断：对期望成功的用例比较产生的字节与声明的目标。
	// 错误但接近的结果仍会在上面的门中失败，尽管这里报告高相似度。
	if c.WantErrContains == "" && c.WantResult != "" && applyErr == nil {
		res.Similarity = EditSimilarity(final, c.WantResult)
		if res.Similarity >= 0 {
			res.scored = true
			res.tokenExact = TokenExactMatch(final, c.WantResult)
		}
	}
	return res
}

// RunEditBench 运行所有用例并聚合结果。顺序被保留
// 且不触碰全局状态，因此重复调用完全相同。
func RunEditBench(cases []EditCase) EditBenchReport {
	report := EditBenchReport{ByMode: make(map[string]ModeStat)}
	var simSum float64
	var scored int
	for _, c := range cases {
		report.Total++
		result := RunEditCase(c)
		stat := report.ByMode[result.Mode]
		if result.Passed {
			report.Passed++
			stat.Passed++
		} else {
			report.Failed++
			stat.Failed++
			report.Failures = append(report.Failures, result)
		}
		if result.scored {
			stat.similaritySum += result.Similarity
			stat.similarityN++
			exact := boolToInt(result.tokenExact)
			stat.ExactMatches += exact
			report.ExactMatches += exact
			simSum += result.Similarity
			scored++
		}
		report.ByMode[result.Mode] = stat
	}
	report.Scored = scored
	if scored > 0 {
		report.MeanSimilarity = simSum / float64(scored)
	}
	for mode, stat := range report.ByMode {
		if stat.similarityN > 0 {
			stat.MeanSimilarity = stat.similaritySum / float64(stat.similarityN)
		}
		report.ByMode[mode] = stat
	}
	return report
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// String 渲染紧凑、确定性的摘要，带每种模式的通过率。
// 末尾的 ES/EM 段是诊断性的：上面的通过率是唯一
// 权威门且完全不使用相似度信号计算。
func (r EditBenchReport) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "editbench: %d/%d passed (%.1f%%)", r.Passed, r.Total, passRate(r.Passed, r.Total))
	modes := make([]string, 0, len(r.ByMode))
	for mode := range r.ByMode {
		modes = append(modes, mode)
	}
	sort.Strings(modes)
	for _, mode := range modes {
		stat := r.ByMode[mode]
		total := stat.Passed + stat.Failed
		fmt.Fprintf(&b, " | %s %d/%d (%.1f%%)", mode, stat.Passed, total, passRate(stat.Passed, total))
	}
	// ES = 平均编辑相似度，EM = token 精确匹配数。两者都按
	// 模式报告，仅作诊断提示；近乎完美的 ES 仍会在权威内容检查漏掉时使其用例失败。
	// 用例失败，即使权威内容检查漏掉。
	fmt.Fprintf(&b, "\nDIAGNOSTIC mean ES / EM (not authoritative): overall ES=%.4f EM=%d/%d",
		r.MeanSimilarity, r.ExactMatches, r.Scored)
	for _, mode := range modes {
		stat := r.ByMode[mode]
		fmt.Fprintf(&b, "\n  %s: ES=%.4f EM=%d/%d", mode, stat.MeanSimilarity, stat.ExactMatches, stat.similarityN)
	}
	return b.String()
}

func passRate(passed, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(passed) * 100 / float64(total)
}

// buildCurrentContent 模拟模型读取文件后对其所做的编辑。
// ShiftLines 紧接在匹配目标前插入，使行号
// 偏移而目标文本本身保持完整（内容锚定必须胜出）。
func buildCurrentContent(c EditCase) string {
	if c.ShiftLines == "" {
		return c.Original
	}
	target := c.Old
	if c.Mode == modeHashline && c.Anchor != "" {
		target = c.Anchor
	}
	idx := strings.Index(c.Original, target)
	if idx < 0 {
		return c.ShiftLines + c.Original
	}
	return c.Original[:idx] + c.ShiftLines + c.Original[idx:]
}

// gradeEditCase 将观察到的结果与声明的期望比较。
func gradeEditCase(c EditCase, final string, err error) (bool, string, string) {
	gotErr := ""
	if err != nil {
		gotErr = editbenchOneLine(err.Error())
	}
	if c.WantErrContains != "" {
		if err == nil {
			return false, "success: " + editbenchClip(final), "expected an error containing " + strconv.Quote(c.WantErrContains) + ", but the edit succeeded"
		}
		if !strings.Contains(err.Error(), c.WantErrContains) {
			return false, "error: " + gotErr, "error does not contain " + strconv.Quote(c.WantErrContains)
		}
		return true, "error: " + gotErr, ""
	}
	if err != nil {
		return false, "error: " + gotErr, "expected success, but the edit failed: " + gotErr
	}
	if c.WantResult != "" && !strings.Contains(final, c.WantResult) {
		return false, "success: " + editbenchClip(final), "result does not contain " + strconv.Quote(c.WantResult)
	}
	return true, "success: " + editbenchClip(final), ""
}

func editbenchOneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func editbenchClip(s string) string {
	// 复用 clip 使此格式化器不会漂移成字节索引切片并
	// 像以前那样切开多字节 rune。
	return clip(s, 160)
}

// ---- 相似度诊断（仅标准库）----

// editSimMaxRunes 限制 Levenshtein 的 DP，使病态输入无法
// 无限制地分配或循环。在上限以内的输入正常评分；
// 更大的输入返回 -1 哨兵，调用方将其标为"unbounded"并
// 从均值中排除。
const editSimMaxRunes = 20000

// Levenshtein 返回 a 与 b 之间的 rune 级编辑距离，插入、删除和替换各花费 1。它是对称的且按
// 插入、删除和替换各花费 1。它是对称的且按
// rune 工作，因此 CJK 文本从不会在字符中间被切开。它返回 -1
// rune 工作，因此 CJK 文本从不会在字符中间被切开。当任一输入超过 editSimMaxRunes 个 rune 时返回 -1；
// 调用方必须把 -1 当作"unbounded"哨兵，绝不当作距离。
func Levenshtein(a, b string) int {
	ar, br := []rune(a), []rune(b)
	if len(ar) > editSimMaxRunes || len(br) > editSimMaxRunes {
		return -1
	}
	// 保持 DP 行尽可能短：较短字符串的每个 rune 一行，
	// 无论它来自哪个参数。
	if len(ar) < len(br) {
		ar, br = br, ar
	}
	prev := make([]int, len(br)+1)
	cur := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i, ca := range ar {
		cur[0] = i + 1
		for j, cb := range br {
			cost := 1
			if ca == cb {
				cost = 0
			}
			best := prev[j+1] + 1 // delete
			if ins := cur[j] + 1; ins < best {
				best = ins // insert
			}
			if sub := prev[j] + cost; sub < best {
				best = sub // substitute, or match at zero cost
			}
			cur[j+1] = best
		}
		prev, cur = cur, prev
	}
	return prev[len(br)]
}

// EditSimilarity 返回 1 - distance/max(rune 长度)，范围 [0,1]；两个空字符串
// 得分为 1。当任一输入超过 editSimMaxRunes 时返回 -1（Levenshtein 的 unbounded 哨兵）；调用方必须把该情况标为
// 当任一输入超过 editSimMaxRunes 时：调用方必须把该情况
// "unbounded"并从任何均值中排除。
func EditSimilarity(a, b string) float64 {
	dist := Levenshtein(a, b)
	if dist < 0 {
		return -1
	}
	la, lb := len([]rune(a)), len([]rune(b))
	maxLen := la
	if lb > maxLen {
		maxLen = lb
	}
	if maxLen == 0 {
		return 1
	}
	sim := 1 - float64(dist)/float64(maxLen)
	if sim < 0 {
		return 0
	}
	if sim > 1 {
		return 1
	}
	return sim
}

// TokenExactMatch 按非字母/非数字 rune 拆分两个字符串并报告
// 所得 token 序列是否完全相同。保留 Unicode 字母（含汉字）和数字；忽略空白和标点。顺序
// 含汉字）和数字被保留；空白和标点被忽略。顺序
// 有影响，因此 "a b" 与 "b a" 不是精确 token 匹配。
func TokenExactMatch(a, b string) bool {
	ta, tb := editTokens(a), editTokens(b)
	if len(ta) != len(tb) {
		return false
	}
	for i := range ta {
		if ta[i] != tb[i] {
			return false
		}
	}
	return true
}

func editTokens(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}
