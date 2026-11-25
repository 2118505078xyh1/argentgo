package app

import (
	"reflect"
	"strings"
	"testing"

	"example.com/argentgo/internal/session"
)

// 本文件是 R17「默认路径可靠性方案 A」的守卫层，只新增测试，不触碰任何生产代码。
// 方案 A 考虑把 preserve_summary_sections 与 keep_implicit_context 的默认翻转开启。
// 翻转前本文件验证「显式开启可达」；翻转后同一组断言自动升级为「默认路径保真」，
// 同时始终锁定两条不可动摇的底线：显式全关路径逐字节不变、compaction_fidelity 仍默认关。

// compactionGuardRegistry 按 DefaultOptIns 的当前取值构造注册表，模拟 Execute 装配面。
// overrides 用于显式覆盖个别键：默认值翻转后本 helper 无需修改即可反映新的默认形态。
func compactionGuardRegistry(overrides map[OptInKey]bool) *OptInRegistry {
	reg := new(OptInRegistry)
	for key, on := range DefaultOptIns() {
		reg.Set(key, on)
	}
	for key, on := range overrides {
		reg.Set(key, on)
	}
	return reg
}

// compactionGuardAllOffRegistry 显式把全部键写为 false。
// 它刻意不读取 DefaultOptIns：即使将来压缩 wire 默认翻转，这条路径也必须是接线前的
// 逐字节行为，作为「无压缩路径仍逐字节不变」的稳定基线。
func compactionGuardAllOffRegistry() *OptInRegistry {
	reg := new(OptInRegistry)
	for _, key := range allOptInKeys {
		reg.Set(key, false)
	}
	return reg
}

// assertContextPreserved 断言重建摘要确实保留了两条最易丢失的上下文：
// 原始 Goal 段的中文内容，以及从被丢弃工具结果中抽取的隐式失败条目。
func assertContextPreserved(t *testing.T, summary string) {
	t.Helper()
	if !strings.Contains(summary, "## Goal") {
		t.Error("摘要缺少 Goal 段标题")
	}
	// buildCompactionSummary 会把 Goal 写成 "- (none)"；这里命中的是 orig 里的原始目标，
	// 因此该断言为真是 preserve wire 生效的直接证据，而不是恒真检查。
	if !strings.Contains(summary, "修复登录鉴权（中文目标）") {
		t.Errorf("摘要未保留原始 Goal 内容: %q", summary)
	}
	if !strings.Contains(summary, "Tool activity") {
		t.Errorf("摘要未保留原始 Tool activity 段: %q", summary)
	}
	// "数据库连接失败（隐式）" 只存在于 dropped 工具结果中，显式 Failures 用的是另一串，
	// 命中即证明隐式失败条目确实被并入摘要。
	if !strings.Contains(summary, "数据库连接失败（隐式）") {
		t.Errorf("摘要未注入隐式失败条目: %q", summary)
	}
}

// TestDefaultCompactionPreservesContext 证明方案 A 的目标语义：有损摘要经过默认压缩路径
// 重建后仍保留目标与隐式失败上下文。默认尚未翻转时，先锁定「显式开启可达」作为语义参照；
// 一旦 DefaultOptIns 翻转这两个键，同一测试自动追加对默认注册表的等值断言。
func TestDefaultCompactionPreservesContext(t *testing.T) {
	// 有损摘要：buildCompactionSummary 会把 Goal 硬编码成 "(none)" 且完全丢弃 Tool activity。
	orig := "## Compaction\n" +
		"\n## Goal\n- 修复登录鉴权（中文目标）\n" +
		"\n## Tool activity\n- read foo.go\n" +
		"\n## Next steps\n- 补单元测试\n"

	// 被丢弃条目：一条带失败信号的工具结果，隐式上下文 wire 应把其预览并入 Failures。
	dropped := []session.Entry{
		{Role: "tool", Name: "bash", Content: "[exit 1] 数据库连接失败（隐式）\n"},
	}

	c := session.Compaction{
		Dropped:   1,
		Keep:      6,
		Files:     []string{"internal/auth.go"},
		Failures:  []string{"auth: permission denied"},
		NextSteps: []string{"补单元测试"},
	}

	// 显式开启两个「保留上下文」wire 的注册表作为语义参照，不依赖默认值当前形态。
	ctxReg := compactionGuardRegistry(map[OptInKey]bool{
		OptInPreserveSummarySections: true,
		OptInKeepImplicitContext:     true,
	})
	want := rebuildCompactionSummary(c, dropped, orig, ctxReg)
	assertContextPreserved(t, want)

	defaults := DefaultOptIns()
	if defaults[OptInPreserveSummarySections] && defaults[OptInKeepImplicitContext] {
		// 默认已翻转：默认压缩路径必须直接达到与显式开启逐字节相同的结果。
		got := rebuildCompactionSummary(c, dropped, orig, compactionGuardRegistry(nil))
		if got != want {
			t.Fatalf("默认压缩未保留上下文:\n got  %q\n want %q", got, want)
		}
		return
	}
	// 默认尚未翻转：本测试当前依赖「显式开启可达」，默认值翻转为 true 后自动升级为
	// 对默认路径的守卫，无需修改本文件。
	t.Log("默认尚未开启 preserve_summary_sections / keep_implicit_context；" +
		"当前仅验证显式开启可达，默认值翻转后本测试自动覆盖默认路径")
}

// TestNoCompactionPathStillByteExact 锁定方案 A 的底线：无论默认值是否翻转，显式全关
// reg 下 rebuildCompactionSummary 必须与接线前的 buildCompactionSummary 逐字节相同。
// 它复用并补充 TestGuardRebuildSummaryByteExactWhenOff，额外覆盖「默认值翻转后强制关」的形态。
func TestNoCompactionPathStillByteExact(t *testing.T) {
	c := session.Compaction{
		Dropped:   7,
		Keep:      6,
		Files:     []string{"internal/auth.go"},
		Failures:  []string{"auth: permission denied"},
		NextSteps: []string{"补单元测试"},
	}
	orig := "## Compaction\n" +
		"\n## Goal\n- 修复登录鉴权\n" +
		"\n## Tool activity\n- read foo.go\n" +
		"\n## Next steps\n- 补单元测试\n"
	dropped := []session.Entry{
		{Role: "tool", Name: "bash", Content: "数据库连接失败\n"},
		{Role: "assistant", Content: "下一步我会补充中文单元测试并复核。"},
	}

	for _, tc := range []struct {
		name string
		reg  *OptInRegistry
	}{
		{"nil registry", nil},
		{"all-off registry", compactionGuardAllOffRegistry()},
		// 即便未来默认翻转，把三个压缩 wire 显式写回 false 也必须回到原始字节。
		{"defaults with compaction wires forced off", compactionGuardRegistry(map[OptInKey]bool{
			OptInPreserveSummarySections: false,
			OptInKeepImplicitContext:     false,
			OptInCompactionFidelity:      false,
		})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := rebuildCompactionSummary(c, dropped, orig, tc.reg)
			want := buildCompactionSummary(c)
			if got != want {
				t.Fatalf("显式全关时重建摘要字节变化:\n got  %q\n want %q", got, want)
			}
		})
	}
}

// TestFidelityRemainsOptIn 守护方案 A 的边界：preserve / implicit 可以默认开，
// 但 compaction_fidelity 是告警噪声，必须保持默认关闭，不得随方案 A 一起翻转。
func TestFidelityRemainsOptIn(t *testing.T) {
	// 四要素全为占位符，覆盖度为 0，必定被判为有损。
	lossy := "## Compaction\n- dropped 7 message(s), kept the last 6\n" +
		"\n## Goal\n- (none)\n" +
		"\n## Files changed\n- (none)\n" +
		"\n## Failures\n- (none)\n" +
		"\n## Next steps\n- (none)\n"
	if compactFidelityNotice(lossy) == "" {
		t.Fatal("测试前置失败：有损摘要应触发保真告警")
	}

	if DefaultOptIns()[OptInCompactionFidelity] {
		t.Fatal("compaction_fidelity 默认被开启：它是告警噪声，不应默认开启")
	}
	// 默认注册表（即使压缩 wire 已翻转）也不得挂接保真观察者。
	if got := newCompactionFidelityObserver(compactionGuardRegistry(nil)); got != nil {
		t.Fatal("默认注册表仍挂接了 compaction fidelity 观察者")
	}
	// 显式全关同样不挂接。
	if got := newCompactionFidelityObserver(compactionGuardAllOffRegistry()); got != nil {
		t.Fatal("显式全关注册表仍挂接了 compaction fidelity 观察者")
	}
	// 只有显式开启才挂接，并在有损摘要上产出告警。
	on := compactionGuardRegistry(map[OptInKey]bool{OptInCompactionFidelity: true})
	observer := newCompactionFidelityObserver(on)
	if observer == nil {
		t.Fatal("显式开启后未挂接 compaction fidelity 观察者")
	}
	if got := observer(lossy); got == "" {
		t.Fatal("显式开启后观察者在有损摘要上未产生告警")
	}
}

// TestCompactionGuardPureCJKIdempotent 验证 wire 全开时的纯函数契约：确定性、不改入参、
// CJK 字节完整，以及隐式合并对已带标记的摘要保持幂等。
func TestCompactionGuardPureCJKIdempotent(t *testing.T) {
	c := session.Compaction{
		Dropped:   2,
		Keep:      6,
		Files:     []string{"internal/auth.go"},
		Failures:  []string{"鉴权失败：权限不足"},
		NextSteps: []string{"补充单元测试"},
	}
	orig := "## Compaction\n" +
		"\n## Goal\n- 修复登录鉴权\n" +
		"\n## Tool activity\n- read foo.go\n"
	dropped := []session.Entry{
		{Role: "tool", Name: "bash", Content: "[exit 1] 连接失败（隐式信号）\n"},
		{Role: "assistant", Content: "下一步我会补充中文单元测试并复核。"},
	}
	reg := compactionGuardRegistry(map[OptInKey]bool{
		OptInPreserveSummarySections: true,
		OptInKeepImplicitContext:     true,
	})

	// 入参快照：纯函数路径绝不修改 Compaction 与 dropped 切片及其元素。
	cBefore := c
	droppedBefore := append([]session.Entry(nil), dropped...)

	first := rebuildCompactionSummary(c, dropped, orig, reg)
	second := rebuildCompactionSummary(c, dropped, orig, reg)
	if first != second {
		t.Fatalf("重复调用结果不一致（非确定性）:\n first  %q\n second %q", first, second)
	}
	if !reflect.DeepEqual(c, cBefore) {
		t.Fatalf("rebuildCompactionSummary 修改了入参 c:\n got  %+v\n want %+v", c, cBefore)
	}
	if !reflect.DeepEqual(dropped, droppedBefore) {
		t.Fatalf("rebuildCompactionSummary 修改了入参 dropped:\n got  %+v\n want %+v", dropped, droppedBefore)
	}

	// CJK 完整性：所有中文片段按整串出现，且不含 UTF-8 替换符（未从字符中间截断）。
	for _, want := range []string{"修复登录鉴权", "鉴权失败：权限不足", "补充单元测试", "连接失败（隐式信号）"} {
		if !strings.Contains(first, want) {
			t.Errorf("CJK 片段 %q 未完整出现在摘要: %q", want, first)
		}
	}
	if strings.Contains(first, "\uFFFD") {
		t.Errorf("摘要出现 UTF-8 替换符，疑似按字节截断: %q", first)
	}

	// 幂等：隐式合并对已带标记的摘要不再追加。
	if got := enrichWithImplicit(first, dropped, reg); got != first {
		t.Fatalf("enrichWithImplicit 非幂等:\n got  %q\n want %q", got, first)
	}
}
