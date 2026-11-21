package eval

import (
	"reflect"
	"testing"
	"unicode/utf8"
)

// namedScenarios 用给定名字构造最小场景切片，名字即覆盖统计的匹配键。
func namedScenarios(names ...string) []Scenario {
	out := make([]Scenario, len(names))
	for i, name := range names {
		out[i] = Scenario{Name: name}
	}
	return out
}

// TestAnalyzeCoverageMultiDimension 覆盖多维度统计：各维度独立计数，
// 一个场景可同时计入多个维度，Total 为场景总数。
func TestAnalyzeCoverageMultiDimension(t *testing.T) {
	scenarios := namedScenarios("s1", "s2", "s3", "s4")
	dimensionTags := map[string][]string{
		"路径逃逸": {"s1", "s2", "s4"},
		"审批绕过": {"s2", "s3"},
		"取消响应": {"s1", "s3", "s4"},
	}
	got := AnalyzeCoverage(scenarios, dimensionTags)
	if got.TotalScenarios != 4 {
		t.Errorf("TotalScenarios = %d, want 4", got.TotalScenarios)
	}
	wantBy := map[string]int{
		"路径逃逸": 3,
		"审批绕过": 2,
		"取消响应": 3,
	}
	if !reflect.DeepEqual(got.ByDimension, wantBy) {
		t.Errorf("ByDimension = %#v, want %#v", got.ByDimension, wantBy)
	}
	if len(got.Gaps) != 0 {
		t.Errorf("Gaps = %#v, want empty (all dimensions covered)", got.Gaps)
	}
}

// TestAnalyzeCoverageGaps 零覆盖维度须列入 Gaps，且 Gaps 按字节序升序；
// 部分覆盖的维度不得出现在 Gaps 中。
func TestAnalyzeCoverageGaps(t *testing.T) {
	scenarios := namedScenarios("s1", "s2")
	dimensionTags := map[string][]string{
		"路径逃逸": {"s1"},       // 覆盖 1
		"审批绕过": {"ghost"},    // 标签指向不存在的场景 → 0
		"取消响应": {"s1", "s2"}, // 覆盖 2
		"状态漂移": {},           // 空标签 → 0
	}
	got := AnalyzeCoverage(scenarios, dimensionTags)
	wantGaps := []string{"审批绕过", "状态漂移"}
	if !reflect.DeepEqual(got.Gaps, wantGaps) {
		t.Errorf("Gaps = %#v, want %#v", got.Gaps, wantGaps)
	}
	if got.ByDimension["审批绕过"] != 0 || got.ByDimension["状态漂移"] != 0 {
		t.Errorf("zero-cover dimensions must be retained with 0: %#v", got.ByDimension)
	}
	if got.ByDimension["路径逃逸"] != 1 || got.ByDimension["取消响应"] != 2 {
		t.Errorf("covered dimensions miscounted: %#v", got.ByDimension)
	}
}

// TestAnalyzeCoverageCJK 维度名与场景名均为中文时，统计与 gaps 仍正确，
// 且所有输出都是合法 UTF-8、含汉字（CJK 安全）。
func TestAnalyzeCoverageCJK(t *testing.T) {
	scenarios := namedScenarios("拒绝越界写入", "取消正在运行的任务", "脱敏审计日志")
	dimensionTags := map[string][]string{
		"路径逃逸": {"拒绝越界写入"},
		"审批绕过": {"拒绝越界写入"},
		"取消响应": {"取消正在运行的任务"},
		"脱敏缺失": {"脱敏审计日志"},
		"并发竞态": {"不存在的场景"},
	}
	got := AnalyzeCoverage(scenarios, dimensionTags)
	if got.TotalScenarios != 3 {
		t.Errorf("TotalScenarios = %d, want 3", got.TotalScenarios)
	}
	if !reflect.DeepEqual(got.Gaps, []string{"并发竞态"}) {
		t.Errorf("Gaps = %#v, want [并发竞态]", got.Gaps)
	}
	// 校验所有维度名与 gap 都是合法 UTF-8 且含汉字。
	checks := make([]string, 0, len(got.ByDimension)+len(got.Gaps))
	for dimension := range got.ByDimension {
		checks = append(checks, dimension)
	}
	checks = append(checks, got.Gaps...)
	for _, s := range checks {
		if !utf8.ValidString(s) {
			t.Errorf("invalid UTF-8: %q", s)
		}
		if !hasHan(s) {
			t.Errorf("expected Chinese dimension name, got %q", s)
		}
	}
}

// TestAnalyzeCoverageEmptyScenarios 空场景集：每个维度覆盖 0，全部成为 gaps。
func TestAnalyzeCoverageEmptyScenarios(t *testing.T) {
	dimensionTags := map[string][]string{
		"路径逃逸": {"s1"},
		"审批绕过": {"s2"},
	}
	got := AnalyzeCoverage(nil, dimensionTags)
	if got.TotalScenarios != 0 {
		t.Errorf("TotalScenarios = %d, want 0", got.TotalScenarios)
	}
	wantBy := map[string]int{"路径逃逸": 0, "审批绕过": 0}
	if !reflect.DeepEqual(got.ByDimension, wantBy) {
		t.Errorf("ByDimension = %#v, want %#v", got.ByDimension, wantBy)
	}
	// sort.Strings 按 UTF-8 字节序："审"(U+5BA1) < "路"(U+8DEF)。
	if !reflect.DeepEqual(got.Gaps, []string{"审批绕过", "路径逃逸"}) {
		t.Errorf("Gaps = %#v, want all dimensions", got.Gaps)
	}
}

// TestAnalyzeCoverageEmptyDimensions 空维度表：结果为空但非 nil 的 map/slice。
func TestAnalyzeCoverageEmptyDimensions(t *testing.T) {
	got := AnalyzeCoverage(namedScenarios("s1"), nil)
	if got.TotalScenarios != 1 {
		t.Errorf("TotalScenarios = %d, want 1", got.TotalScenarios)
	}
	if got.ByDimension == nil || len(got.ByDimension) != 0 {
		t.Errorf("ByDimension = %#v, want empty non-nil map", got.ByDimension)
	}
	if got.Gaps == nil || len(got.Gaps) != 0 {
		t.Errorf("Gaps = %#v, want empty non-nil slice", got.Gaps)
	}
}

// TestAnalyzeCoverageNoInputMutation 函数不得修改入参：场景切片、每个维度的
// 标签列表及其内容在调用前后逐字段一致。
func TestAnalyzeCoverageNoInputMutation(t *testing.T) {
	scenarios := namedScenarios("s1", "s2")
	dimensionTags := map[string][]string{
		"甲": {"s1", "s2"},
		"乙": {"s2"},
	}
	scenariosBefore := append([]Scenario(nil), scenarios...)
	tagsBefore := make(map[string][]string, len(dimensionTags))
	for k, v := range dimensionTags {
		tagsBefore[k] = append([]string(nil), v...)
	}

	AnalyzeCoverage(scenarios, dimensionTags)

	if !reflect.DeepEqual(scenarios, scenariosBefore) {
		t.Errorf("scenarios mutated:\n got: %#v\nwant: %#v", scenarios, scenariosBefore)
	}
	if !reflect.DeepEqual(dimensionTags, tagsBefore) {
		t.Errorf("dimensionTags mutated:\n got: %#v\nwant: %#v", dimensionTags, tagsBefore)
	}
}

// TestAnalyzeCoverageDeterminism 相同入参多次调用结果逐字段一致（含 Gaps 顺序）。
func TestAnalyzeCoverageDeterminism(t *testing.T) {
	scenarios := namedScenarios("s1", "s2", "s3")
	dimensionTags := map[string][]string{
		"乙": {"s2"},
		"甲": {"s1", "s2"},
		"丙": {"ghost"},
		"丁": {"s1"},
	}
	first := AnalyzeCoverage(scenarios, dimensionTags)
	for i := 0; i < 20; i++ {
		got := AnalyzeCoverage(scenarios, dimensionTags)
		if !reflect.DeepEqual(got, first) {
			t.Fatalf("call %d not deterministic:\n got: %#v\nwant: %#v", i, got, first)
		}
	}
}

// TestAnalyzeCoverageNoSharedState 返回结构与入参及后续调用均无共享可变状态：
// 外部改写结果的 map/slice 不影响新一次调用。
func TestAnalyzeCoverageNoSharedState(t *testing.T) {
	scenarios := namedScenarios("s1")
	dimensionTags := map[string][]string{"甲": {"s1"}, "乙": {"ghost"}}
	a := AnalyzeCoverage(scenarios, dimensionTags)
	a.ByDimension["甲"] = 99
	a.Gaps[0] = "（被外部修改）"

	b := AnalyzeCoverage(scenarios, dimensionTags)
	if b.ByDimension["甲"] != 1 {
		t.Errorf("ByDimension shares state: got %d, want 1", b.ByDimension["甲"])
	}
	if !reflect.DeepEqual(b.Gaps, []string{"乙"}) {
		t.Errorf("Gaps shares state: %#v", b.Gaps)
	}
}

// TestAnalyzeCoverageTagDedup 同一维度标签列表内的重复项不得夸大覆盖数。
func TestAnalyzeCoverageTagDedup(t *testing.T) {
	scenarios := namedScenarios("s1", "s2")
	dimensionTags := map[string][]string{
		"甲": {"s1", "s1", "s2", "s1"},
	}
	got := AnalyzeCoverage(scenarios, dimensionTags)
	if got.ByDimension["甲"] != 2 {
		t.Errorf("ByDimension[甲] = %d, want 2 (dedup tags)", got.ByDimension["甲"])
	}
}
