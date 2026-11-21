package eval

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// 本文件是「变异原语行为守卫」测试，关注 MutatePrompt / ScenarioFingerprint /
// SelectVariants 对 A/B 配对可靠性的契约，与 scenario_mutate_test.go 的功能测试互补：
// 功能测试断言「返回什么」，守卫测试断言「同一输入永远返回同一结果、不同种子/场景
// 必须可区分、且绝不污染入参」——这三点任何一点被打破，variant_ab 的两臂就无法
// 稳定复现或会错误配对。全部断言只依赖入参，不联网、不读时钟、不用真实模型。

// guardSeeds 是守卫测试共用的种子集合。用多个种子做「集合基数 > 1」的判定，
// 比绑定某一对种子更稳健：只要哈希排序/标记不是常量，测试就能生效。
var guardSeeds = []string{"s0", "s1", "s2", "s3", "s4", "s5", "s6", "s7"}

// guardPrompts 覆盖 ASCII、CJK、首尾/连续空白与空串，确保确定性矩阵包含
// A/B 场景里实际会出现的 prompt 形态。
var guardPrompts = []string{
	"plain ascii prompt",
	"  前导与尾随空白  ",
	"多行\n\t混合\t空白",
	"请读取 notes.txt 并总结要点",
	"",
}

// TestGuardMutatePromptDeterministicMatrix 验证所有 (prompt, seed, mode) 组合
// 重复调用逐字节一致；并对每种 mode 复核其核心不变量。A/B 两臂要在不同进程、
// 不同时刻复现同一个变体，确定性是前提条件。
func TestGuardMutatePromptDeterministicMatrix(t *testing.T) {
	modes := []string{"none", "trim", "upper-wrap", "unknown-mode"}
	for _, prompt := range guardPrompts {
		for _, seed := range guardSeeds {
			for _, mode := range modes {
				first := MutatePrompt(prompt, seed, mode)
				for i := 0; i < 3; i++ {
					if again := MutatePrompt(prompt, seed, mode); again != first {
						t.Fatalf("MutatePrompt(%q, %q, %q) 不确定：%q vs %q",
							prompt, seed, mode, first, again)
					}
				}

				switch mode {
				case "trim":
					if first != strings.Join(strings.Fields(prompt), " ") {
						t.Fatalf("trim 未按 Unicode 空白折叠：%q -> %q", prompt, first)
					}
				case "upper-wrap":
					if !strings.Contains(first, prompt) {
						t.Fatalf("upper-wrap 未逐字节保留原文：prompt=%q got=%q", prompt, first)
					}
				}
			}
		}
	}
}

// TestGuardMutatePromptSeedChangesUpperWrapTag 验证 upper-wrap 的种子敏感性：
// 多个种子必须产生多个不同的标记，且标记是 8 位小写十六进制、首尾标记一致。
// 只有种子真正改变变体身份，A/B 的「基线臂 / 候选臂」才不会退化成同一个 prompt。
func TestGuardMutatePromptSeedChangesUpperWrapTag(t *testing.T) {
	const prompt = "同样的原文 同样的语义"
	seen := make(map[string]bool, len(guardSeeds))
	for _, seed := range guardSeeds {
		out := MutatePrompt(prompt, seed, "upper-wrap")

		begin := "<<<VARIANT hash="
		end := "<<<END_VARIANT hash="
		if !strings.HasPrefix(out, begin) || !strings.Contains(out, "\n"+end) {
			t.Fatalf("upper-wrap 标记缺失：seed=%q out=%q", seed, out)
		}
		beginTag, endTag := guardTagAfter(out, begin), guardTagAfter(out, end)
		if beginTag != endTag {
			t.Fatalf("首尾标记不一致：%q vs %q (seed=%q)", beginTag, endTag, seed)
		}
		if len(beginTag) != 8 || strings.ToLower(beginTag) != beginTag {
			t.Fatalf("标记不是 8 位小写十六进制：%q (seed=%q)", beginTag, seed)
		}
		if !strings.Contains(out, "\n"+prompt+"\n") {
			t.Fatalf("标记之间未逐字节保留原文：seed=%q out=%q", seed, out)
		}

		seen[out] = true
	}
	// 8 个种子里至少一半要产出不同变体；既证明种子参与标记计算，
	// 又不会因哈希碰撞出现偶发红。
	if len(seen) < len(guardSeeds)/2 {
		t.Fatalf("upper-wrap 种子敏感性不足：%d 个种子只产出 %d 种变体", len(guardSeeds), len(seen))
	}
}

// TestGuardMutatePromptFullWidthSpaceTrim 覆盖注释承诺的 CJK 空白行为：全角空格、
// 制表与换行都折叠为单个 ASCII 空格，首尾空白被去掉。这是中文 prompt 走 trim
// 变体时的关键边界，折叠行为改变会让两臂场景文本意外分叉。
func TestGuardMutatePromptFullWidthSpaceTrim(t *testing.T) {
	tests := []struct {
		prompt string
		want   string
	}{
		{"你好\u3000世界", "你好 世界"},
		{"\u3000首\u3000尾\u3000", "首 尾"},
		{"第一行\n\u3000\u3000第二行\t第三行", "第一行 第二行 第三行"},
	}
	for _, tc := range tests {
		if got := MutatePrompt(tc.prompt, "irrelevant", "trim"); got != tc.want {
			t.Fatalf("trim(%q) = %q, want %q", tc.prompt, got, tc.want)
		}
	}
}

// TestGuardScenarioFingerprintStableAndDistinct 验证指纹对同一场景跨调用稳定、
// 格式为 64 位小写十六进制，且一组含 CJK 的场景两两不碰撞。指纹是两臂配对的
// 唯一身份键，碰撞会直接导致错配。
func TestGuardScenarioFingerprintStableAndDistinct(t *testing.T) {
	scenarios := []struct{ name, prompt string }{
		{"读取笔记", "notes.txt 里写了什么？"},
		{"写入文件", "把结果写到 out.txt"},
		{"搜索代码", "找到 handleRequest 的定义"},
		{"empty", ""},
		{"", "prompt without name"},
	}
	seen := make(map[string]string, len(scenarios))
	for _, s := range scenarios {
		fp := ScenarioFingerprint(s.name, s.prompt)
		if len(fp) != 64 || strings.ToLower(fp) != fp {
			t.Fatalf("指纹格式异常：%q -> %q", s, fp)
		}
		if again := ScenarioFingerprint(s.name, s.prompt); again != fp {
			t.Fatalf("指纹不稳定：%q -> %q vs %q", s, fp, again)
		}
		if prev, ok := seen[fp]; ok {
			t.Fatalf("指纹碰撞：%q 与 %q 都是 %q", prev, s, fp)
		}
		seen[fp] = fmt.Sprint(s)
	}
}

// TestGuardScenarioFingerprintChangesOnAnyFieldEdit 验证 name 或 prompt 任一字段
// 改动都会改变指纹，且长度前缀消除了字段边界歧义。A/B 里若两臂场景内容不同，
// 指纹必须不同，否则配对会掩盖实际差异。
func TestGuardScenarioFingerprintChangesOnAnyFieldEdit(t *testing.T) {
	base := ScenarioFingerprint("场景A", "读取文件")
	tests := []struct{ name, prompt, why string }{
		{"场景B", "读取文件", "改名"},
		{"场景A", "写入文件", "改 prompt"},
		{"场景A", "读取文件 ", "prompt 追加空白"},
		{"场景A ", "读取文件", "name 追加空白"},
		{"场", "景A读取文件", "字段边界互换"},
	}
	for _, tc := range tests {
		got := ScenarioFingerprint(tc.name, tc.prompt)
		if got == base {
			t.Fatalf("%s 后指纹未变化：(%q, %q) 仍为 %q", tc.why, tc.name, tc.prompt, got)
		}
	}
}

// TestGuardSelectVariantsStableForScenarioNames 用含 CJK 的场景名验证选择稳定性：
// 重复调用一致、与入参顺序无关、不重复、且每个结果都来自入参。候选集顺序变化
// 不得改变 A/B 的分臂结果。
func TestGuardSelectVariantsStableForScenarioNames(t *testing.T) {
	names := []string{"读取笔记", "写入文件", "搜索代码", "运行测试", "审查差异", "生成报告"}
	seed := "ab-arm-seed"

	want := SelectVariants(names, seed, 4)
	if len(want) != 4 {
		t.Fatalf("SelectVariants 返回 %d 项，want 4", len(want))
	}
	for i := 0; i < 3; i++ {
		if got := SelectVariants(names, seed, 4); !reflect.DeepEqual(got, want) {
			t.Fatalf("选择不稳定：%q vs %q", got, want)
		}
	}
	shuffled := []string{"生成报告", "读取笔记", "审查差异", "写入文件", "搜索代码", "运行测试"}
	if got := SelectVariants(shuffled, seed, 4); !reflect.DeepEqual(got, want) {
		t.Fatalf("选择依赖入参顺序：%q vs %q", got, want)
	}

	allowed := make(map[string]bool, len(names))
	for _, n := range names {
		allowed[n] = true
	}
	picked := make(map[string]bool, len(want))
	for _, n := range want {
		if !allowed[n] {
			t.Fatalf("返回了入参之外的名字：%q", n)
		}
		if picked[n] {
			t.Fatalf("返回重复名字：%q", n)
		}
		picked[n] = true
	}
}

// TestGuardSelectVariantsSeedChangesChoice 验证种子确实影响选择：遍历多个种子，
// 选择结果（有序列表）必须出现至少两种不同组合。若所有种子选同一批场景，
// A/B 分臂将失去区分度。
func TestGuardSelectVariantsSeedChangesChoice(t *testing.T) {
	names := []string{
		"s01", "s02", "s03", "s04", "s05", "s06", "s07", "s08",
		"s09", "s10", "s11", "s12", "s13", "s14", "s15", "s16",
	}
	seen := make(map[string]bool, len(guardSeeds))
	for _, seed := range guardSeeds {
		got := SelectVariants(names, seed, 5)
		seen[strings.Join(got, ",")] = true
	}
	// 种子若参与排序键，8 个种子几乎必然给出多种组合；用半数阈值避免
	// 两个不同种子恰好同序导致的偶发红，同时仍能抓住「常量选择」回归。
	if len(seen) < len(guardSeeds)/2 {
		t.Fatalf("SelectVariants 种子敏感性不足：%d 个种子只产出 %d 种组合", len(guardSeeds), len(seen))
	}
}

// TestGuardMutationPrimitivesDoNotMutateInputs 验证纯函数承诺：SelectVariants
// 不改动 names 切片；FingerprintsFromScenarios 不改动场景；MutatePrompt 只返回
// 新字符串，不触碰传入的场景。任何入参污染都会让复跑 A/B 得到漂移的结果。
func TestGuardMutationPrimitivesDoNotMutateInputs(t *testing.T) {
	names := []string{"zeta", "eta", "theta", "iota"}
	namesBefore := append([]string(nil), names...)
	_ = SelectVariants(names, "seed", 2)
	if !reflect.DeepEqual(names, namesBefore) {
		t.Fatalf("SelectVariants 污染了 names：%q vs %q", names, namesBefore)
	}

	scenarios := []Scenario{
		{Name: "读取笔记", Prompt: "notes.txt 里写了什么？"},
		{Name: "写入文件", Prompt: "把结果写到 out.txt"},
	}
	scenariosBefore := append([]Scenario(nil), scenarios...)
	fps := FingerprintsFromScenarios(scenarios)
	if len(fps) != len(scenarios) {
		t.Fatalf("FingerprintsFromScenarios 映射长度 = %d, want %d", len(fps), len(scenarios))
	}
	if !reflect.DeepEqual(scenarios, scenariosBefore) {
		t.Fatalf("FingerprintsFromScenarios 污染了 scenarios：%+v vs %+v", scenarios, scenariosBefore)
	}

	for _, s := range scenariosBefore {
		_ = MutatePrompt(s.Prompt, "seed", "upper-wrap")
	}
	if !reflect.DeepEqual(scenarios, scenariosBefore) {
		t.Fatalf("MutatePrompt 污染了场景 prompt：%+v vs %+v", scenarios, scenariosBefore)
	}
}

// TestGuardFingerprintSameScenarioAcrossArms 是 A/B 配对的核心守卫：两臂用同一份
// 规范场景生成指纹必须完全一致（可配对）；而把某一臂的 prompt 做变体改写后，
// 该臂的指纹必须全部变化（变体身份可区分）。前者保证「同场景能配上」，后者保证
// 「改写后的场景不会被误当成原场景」。
func TestGuardFingerprintSameScenarioAcrossArms(t *testing.T) {
	canonical := []Scenario{
		{Name: "读取笔记", Prompt: "notes.txt 里写了什么？"},
		{Name: "写入文件", Prompt: "把结果写到 out.txt"},
		{Name: "搜索代码", Prompt: "找到 handleRequest 的定义"},
	}
	fpA := FingerprintsFromScenarios(canonical)
	fpB := FingerprintsFromScenarios(canonical)
	if !reflect.DeepEqual(fpA, fpB) {
		t.Fatalf("两臂规范场景指纹不一致：%+v vs %+v", fpA, fpB)
	}

	// arm B 对每个 prompt 施加不同种子的 upper-wrap 变体。
	mutated := make([]Scenario, len(canonical))
	for i, s := range canonical {
		mutated[i] = s
		mutated[i].Prompt = MutatePrompt(s.Prompt, guardSeeds[i], "upper-wrap")
	}
	fpMutated := FingerprintsFromScenarios(mutated)
	if len(fpMutated) != len(fpA) {
		t.Fatalf("变体臂映射长度 = %d, want %d", len(fpMutated), len(fpA))
	}
	for _, s := range canonical {
		if fpMutated[s.Name] == fpA[s.Name] {
			t.Fatalf("场景 %q 的变体指纹未变化，可能被误配：%q", s.Name, fpA[s.Name])
		}
	}

	// 变体原文必须逐字节嵌在改写后的 prompt 里，保证语义未丢失。
	for i, s := range canonical {
		if !strings.Contains(mutated[i].Prompt, s.Prompt) {
			t.Fatalf("场景 %q 的变体丢失了原文：%q", s.Name, mutated[i].Prompt)
		}
	}
}

// guardTagAfter 返回 s 中 marker 之后紧跟的标记串（截取到第一个非十六进制或结尾）。
func guardTagAfter(s, marker string) string {
	idx := strings.Index(s, marker)
	if idx < 0 {
		return ""
	}
	rest := s[idx+len(marker):]
	end := 0
	for end < len(rest) {
		c := rest[end]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') {
			end++
			continue
		}
		break
	}
	return rest[:end]
}
