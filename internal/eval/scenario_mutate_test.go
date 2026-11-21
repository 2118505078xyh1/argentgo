package eval

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// TestMutatePromptModes 覆盖三种 mode 的核心行为：none 原样、trim 去首尾并折叠、
// upper-wrap 包裹标记且逐字节保留 prompt 原文，以及未知 mode 回退到 none。
func TestMutatePromptModes(t *testing.T) {
	tests := []struct {
		name   string
		prompt string
		seed   string
		mode   string
		want   string
	}{
		{
			name:   "none keeps prompt verbatim",
			prompt: "  hello \n world  ",
			seed:   "seed-a",
			mode:   "none",
			want:   "  hello \n world  ",
		},
		{
			name:   "unknown mode falls back to none",
			prompt: "hello world",
			seed:   "seed-a",
			mode:   "frobnicate",
			want:   "hello world",
		},
		{
			name:   "trim strips edges and collapses whitespace",
			prompt: " \t\n  hello \t world \r\n bye  \n",
			seed:   "seed-a",
			mode:   "trim",
			want:   "hello world bye",
		},
		{
			name:   "trim keeps single spaces and CJK words intact",
			prompt: "\n  你好  世界 \n",
			seed:   "seed-a",
			mode:   "trim",
			want:   "你好 世界",
		},
		{
			name:   "trim of empty prompt is empty",
			prompt: " \t\n ",
			seed:   "seed-a",
			mode:   "trim",
			want:   "",
		},
		{
			name:   "upper-wrap preserves prompt words byte for byte",
			prompt: "MixED CaSE 你好\nsecond line",
			seed:   "seed-a",
			mode:   "upper-wrap",
			// tag 由 seed 的 sha256 前 8 位十六进制决定，下方由 helper 计算。
			want: upperWrapWant("seed-a", "MixED CaSE 你好\nsecond line"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := MutatePrompt(tc.prompt, tc.seed, tc.mode)
			if got != tc.want {
				t.Fatalf("MutatePrompt(%q, %q, %q) = %q, want %q", tc.prompt, tc.seed, tc.mode, got, tc.want)
			}
		})
	}
}

// TestMutatePromptUpperWrapKeepsInnerText 单独验证 upper-wrap 的「不改语义词」承诺：
// 去掉首尾标记后，中间内容必须与原文逐字节相等，且标记本身为大写。
func TestMutatePromptUpperWrapKeepsInnerText(t *testing.T) {
	prompt := "Do not CHANGE this text 你好"
	got := MutatePrompt(prompt, "s1", "upper-wrap")

	if !strings.HasPrefix(got, "<<<VARIANT hash=") {
		t.Fatalf("upper-wrap output missing begin marker: %q", got)
	}
	if !strings.Contains(got, "\n<<<END_VARIANT hash=") {
		t.Fatalf("upper-wrap output missing end marker: %q", got)
	}

	// 去掉第一行标记和最后一行标记，剩下应恰为原文。
	lines := strings.Split(got, "\n")
	if len(lines) < 3 {
		t.Fatalf("upper-wrap output has too few lines: %q", got)
	}
	inner := strings.Join(lines[1:len(lines)-1], "\n")
	if inner != prompt {
		t.Fatalf("upper-wrap changed inner text: got %q, want %q", inner, prompt)
	}
}

// TestMutatePromptDeterministic 验证同样参数重复调用输出一致。
func TestMutatePromptDeterministic(t *testing.T) {
	for _, mode := range []string{"none", "trim", "upper-wrap"} {
		a := MutatePrompt("  hello \n 世界 ", "fixed-seed", mode)
		b := MutatePrompt("  hello \n 世界 ", "fixed-seed", mode)
		if a != b {
			t.Errorf("mode %q is not deterministic: %q vs %q", mode, a, b)
		}
	}
}

// TestMutatePromptSeedSensitivity 验证种子敏感性只作用于 upper-wrap：
// 不同 seed 产生不同标记，而 trim/none 完全忽略 seed。
func TestMutatePromptSeedSensitivity(t *testing.T) {
	prompt := "same prompt"

	a := MutatePrompt(prompt, "seed-a", "upper-wrap")
	b := MutatePrompt(prompt, "seed-b", "upper-wrap")
	if a == b {
		t.Fatalf("upper-wrap should be seed sensitive, both were %q", a)
	}

	for _, mode := range []string{"none", "trim"} {
		x := MutatePrompt(prompt, "seed-a", mode)
		y := MutatePrompt(prompt, "seed-b", mode)
		if x != y {
			t.Errorf("mode %q should ignore seed, got %q vs %q", mode, x, y)
		}
	}
}

// TestScenarioFingerprintStable 验证指纹确实是标准 SHA-256（用独立计算复核），
// 输出为 64 位小写十六进制，且同样的输入永远稳定。
func TestScenarioFingerprintStable(t *testing.T) {
	tests := []struct {
		name   string
		sName  string
		prompt string
	}{
		{name: "ascii", sName: "simple question", prompt: "What is 2+2?"},
		{name: "empty fields", sName: "", prompt: ""},
		{name: "cjk", sName: "场景一", prompt: "请读取文件 你好"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ScenarioFingerprint(tc.sName, tc.prompt)
			want := independentFingerprint(tc.sName, tc.prompt)
			if got != want {
				t.Fatalf("ScenarioFingerprint(%q, %q) = %q, want independent sha256 %q", tc.sName, tc.prompt, got, want)
			}
			if len(got) != 64 || strings.ToLower(got) != got {
				t.Fatalf("fingerprint %q is not 64 lowercase hex chars", got)
			}
			if again := ScenarioFingerprint(tc.sName, tc.prompt); again != got {
				t.Fatalf("fingerprint is not stable: %q vs %q", got, again)
			}
		})
	}
}

// TestScenarioFingerprintCJK 验证中文输入得到稳定且互异的指纹。
func TestScenarioFingerprintCJK(t *testing.T) {
	inputs := []struct{ name, prompt string }{
		{"场景A", "读取文件"},
		{"场景B", "读取文件"},
		{"场景A", "写入文件"},
	}
	seen := make(map[string]bool, len(inputs))
	for _, in := range inputs {
		fp := ScenarioFingerprint(in.name, in.prompt)
		if seen[fp] {
			t.Fatalf("fingerprint collision for %v: %q", in, fp)
		}
		seen[fp] = true
	}
}

// TestScenarioFingerprintFieldBoundary 验证长度前缀消除字段边界歧义：
// ("ab","c") 与 ("a","bc") 不得碰撞。
func TestScenarioFingerprintFieldBoundary(t *testing.T) {
	a := ScenarioFingerprint("ab", "c")
	b := ScenarioFingerprint("a", "bc")
	if a == b {
		t.Fatalf("field boundary collision: both = %q", a)
	}
}

// TestSelectVariantsDeterministic 验证确定性：重复调用一致，且不依赖 names 的传入顺序。
func TestSelectVariantsDeterministic(t *testing.T) {
	names := []string{"alpha", "beta", "gamma", "delta", "epsilon"}
	seed := "select-seed"

	first := SelectVariants(names, seed, 3)
	second := SelectVariants(names, seed, 3)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("SelectVariants is not deterministic: %q vs %q", first, second)
	}

	// 打乱传入顺序后，结果仍应相同（排序键含兜底的名字字典序）。
	shuffled := []string{"gamma", "alpha", "epsilon", "delta", "beta"}
	if got := SelectVariants(shuffled, seed, 3); !reflect.DeepEqual(got, first) {
		t.Fatalf("SelectVariants depends on input order: shuffled=%q, original=%q", got, first)
	}
}

// TestSelectVariantsSeedSensitivity 验证 seed 参与排序键：不同 seed 在足够大的候选集上
// 给出不同的选择结果。
func TestSelectVariantsSeedSensitivity(t *testing.T) {
	names := []string{
		"n01", "n02", "n03", "n04", "n05", "n06", "n07", "n08",
		"n09", "n10", "n11", "n12", "n13", "n14", "n15", "n16",
	}
	a := SelectVariants(names, "seed-a", 4)
	b := SelectVariants(names, "seed-b", 4)
	if reflect.DeepEqual(a, b) {
		t.Fatalf("SelectVariants should be seed sensitive, both were %q", a)
	}
}

// TestSelectVariantsBounds 覆盖边界：n<=0、空/nil names、n 超过长度、n 恰等于长度。
func TestSelectVariantsBounds(t *testing.T) {
	names := []string{"a", "b", "c"}

	tests := []struct {
		name  string
		names []string
		n     int
		want  []string
	}{
		{name: "zero n returns empty", names: names, n: 0, want: []string{}},
		{name: "negative n returns empty", names: names, n: -1, want: []string{}},
		{name: "empty names returns empty", names: []string{}, n: 2, want: []string{}},
		{name: "nil names returns empty", names: nil, n: 2, want: []string{}},
		{name: "n greater than length returns all", names: names, n: 99, want: []string{"a", "b", "c"}},
		{name: "n equals length returns all", names: names, n: 3, want: []string{"a", "b", "c"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SelectVariants(tc.names, "seed", tc.n)
			if got == nil {
				t.Fatal("SelectVariants returned nil, want non-nil slice")
			}
			// 选择顺序由哈希排序决定，这里只校验成员集合与长度。
			if !sameMembers(got, tc.want) {
				t.Fatalf("SelectVariants(%q, seed, %d) = %q, want members %q", tc.names, tc.n, got, tc.want)
			}
		})
	}
}

// TestSelectVariantsDoesNotMutateInput 验证纯函数承诺：调用前后 names 逐项不变。
func TestSelectVariantsDoesNotMutateInput(t *testing.T) {
	names := []string{"zeta", "eta", "theta", "iota"}
	before := append([]string(nil), names...)

	_ = SelectVariants(names, "seed", 2)

	if !reflect.DeepEqual(names, before) {
		t.Fatalf("names was mutated: got %q, want %q", names, before)
	}
}

// TestSelectVariantsSelectsFromInput 验证返回的每个名字都来自输入，且互不重复。
func TestSelectVariantsSelectsFromInput(t *testing.T) {
	names := []string{"one", "two", "three", "four"}
	got := SelectVariants(names, "seed", 3)
	if len(got) != 3 {
		t.Fatalf("SelectVariants returned %d items, want 3", len(got))
	}
	allowed := make(map[string]bool, len(names))
	for _, name := range names {
		allowed[name] = true
	}
	picked := make(map[string]bool, len(got))
	for _, name := range got {
		if !allowed[name] {
			t.Fatalf("SelectVariants returned unknown name %q", name)
		}
		if picked[name] {
			t.Fatalf("SelectVariants returned duplicate name %q", name)
		}
		picked[name] = true
	}
}

// upperWrapWant 是测试中独立计算 upper-wrap 期望值的 helper。
func upperWrapWant(seed, prompt string) string {
	h := sha256.Sum256([]byte(seed))
	tag := hex.EncodeToString(h[:])[:8]
	return fmt.Sprintf("<<<VARIANT hash=%s>>>\n%s\n<<<END_VARIANT hash=%s>>>", tag, prompt, tag)
}

// independentFingerprint 在测试中独立复算指纹，避免与被测实现共享 helper。
func independentFingerprint(name, prompt string) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%d:%s%d:%s", len(name), name, len(prompt), prompt)))
	return hex.EncodeToString(h[:])
}

// sameMembers 报告两个字符串切片是否包含相同的元素集合（忽略顺序，保留重复计数）。
func sameMembers(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	count := make(map[string]int, len(a))
	for _, s := range a {
		count[s]++
	}
	for _, s := range b {
		count[s]--
		if count[s] < 0 {
			return false
		}
	}
	return true
}
