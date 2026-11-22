package tools

import (
	"reflect"
	"slices"
	"testing"
	"time"
)

// TestTimeoutForTierValues 锁定四档预算值，以及未知档位回退到 normal。
func TestTimeoutForTierValues(t *testing.T) {
	for _, tc := range []struct {
		tier TimeoutTier
		want time.Duration
	}{
		{TimeoutFast, 10 * time.Second},
		{TimeoutNormal, 30 * time.Second},
		{TimeoutLong, 120 * time.Second},
		{TimeoutBatch, 300 * time.Second},
		// 未知名义取值一律回退 normal（30s）。
		{TimeoutTier("bogus"), 30 * time.Second},
		{TimeoutTier(""), 30 * time.Second},
		{TimeoutTier("FAST"), 30 * time.Second}, // 大小写敏感
	} {
		if got := TimeoutForTier(tc.tier); got != tc.want {
			t.Errorf("TimeoutForTier(%q) = %v, want %v", tc.tier, got, tc.want)
		}
	}
}

// TestTimeoutForTierLongAlignsWithBashDefault 锁定 long 与运行时表中 bash
// 默认值一致这一对齐关系（本原语不替换运行时表，仅做诊断展示）。
func TestTimeoutForTierLongAlignsWithBashDefault(t *testing.T) {
	if got, want := TimeoutForTier(TimeoutLong), toolTimeouts["bash"].Def; got != want {
		t.Fatalf("long = %v, want 与运行时 bash 默认值 %v 对齐", got, want)
	}
	// 运行时表中 fetch/web_search/git_status 的 20s 位于 fast(10s) 与
	// normal(30s) 之间，没有精确档位——这是粗粒度分级的预期行为。
	const runtimeShort = 20 * time.Second
	if TimeoutForTier(TimeoutFast) >= runtimeShort || TimeoutForTier(TimeoutNormal) <= runtimeShort {
		t.Fatal("fast/normal 应分别位于运行时 20s 值的两侧")
	}
}

// TestClassifyToolTimeoutKnownTools 验证各档位的显式归属，覆盖任务要求的
// bash/edit/web 等代表工具。
func TestClassifyToolTimeoutKnownTools(t *testing.T) {
	for _, tc := range []struct {
		name string
		want TimeoutTier
	}{
		// fast：本地只读
		{"read", TimeoutFast},
		{"ls", TimeoutFast},
		{"glob", TimeoutFast},
		{"grep", TimeoutFast},
		{"find", TimeoutFast},
		{"git_status", TimeoutFast},
		{"current_time", TimeoutFast},
		// normal：编辑与网络读取
		{"edit", TimeoutNormal},
		{"write", TimeoutNormal},
		{"fetch", TimeoutNormal},
		{"web_fetch", TimeoutNormal},
		{"web_search", TimeoutNormal},
		{"grep_ex", TimeoutNormal},
		// long：命令执行
		{"bash", TimeoutLong},
		{"shell", TimeoutLong},
		{"powershell", TimeoutLong},
		{"exec", TimeoutLong},
		// batch：批量操作
		{"batch_files", TimeoutBatch},
		{"multi_edit", TimeoutBatch},
		{"apply_patch", TimeoutBatch},
		{"repo_map", TimeoutBatch},
	} {
		if got := ClassifyToolTimeout(tc.name); got != tc.want {
			t.Errorf("ClassifyToolTimeout(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestClassifyToolTimeoutUnknownDefaultsToNormal 验证未知工具、空串、
// 大小写变体与带空白的冒名工具一律回退 normal。
func TestClassifyToolTimeoutUnknownDefaultsToNormal(t *testing.T) {
	for _, name := range []string{
		"",
		"unknown_tool",
		"mcp_custom",
		"BASH",      // 大小写敏感，不应命中 bash
		" bash",     // 不做 trim，不应命中 bash
		"bash ",     // 同上
		"web fetch", // 不是 web_fetch
		"fast",      // 档位名本身不是工具名
	} {
		if got := ClassifyToolTimeout(name); got != TimeoutNormal {
			t.Errorf("ClassifyToolTimeout(%q) = %q, want normal", name, got)
		}
	}
}

// TestClassifyToolTimeoutCJKAndUnicodeSafe 验证含 CJK/emoji/组合字符的工具名
// 不触发 panic、不做 rune 截断，并安全回退 normal。
func TestClassifyToolTimeoutCJKAndUnicodeSafe(t *testing.T) {
	names := []string{
		"读取文件",
		"中文目录/工具",
		"🚀fetch",
		"组合e\u0301",
		"中文bash",
		"bash中文",
		"web_fetch中文",
		"读取文件🚀.go",
	}
	for _, name := range names {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("ClassifyToolTimeout(%q) 触发 panic: %v", name, r)
				}
			}()
			if got := ClassifyToolTimeout(name); got != TimeoutNormal {
				t.Errorf("ClassifyToolTimeout(%q) = %q, want normal", name, got)
			}
		}()
	}
}

// TestDescribeTimeoutsStableAndSorted 验证描述的顺序稳定：档位由快到慢，
// 工具名按字典序，且 Seconds 与 TimeoutForTier 一致、覆盖全部已知工具。
func TestDescribeTimeoutsStableAndSorted(t *testing.T) {
	got := DescribeTimeouts()

	wantTiers := []TimeoutTier{TimeoutFast, TimeoutNormal, TimeoutLong, TimeoutBatch}
	if len(got) != len(wantTiers) {
		t.Fatalf("档位数 = %d, want %d", len(got), len(wantTiers))
	}

	seen := make(map[string]bool)
	for i, d := range got {
		if d.Tier != wantTiers[i] {
			t.Errorf("第 %d 档 = %q, want %q", i, d.Tier, wantTiers[i])
		}
		if d.Seconds != int(TimeoutForTier(d.Tier)/time.Second) {
			t.Errorf("档位 %q Seconds = %d, want %d", d.Tier, d.Seconds, TimeoutForTier(d.Tier)/time.Second)
		}
		if !slices.IsSorted(d.Tools) {
			t.Errorf("档位 %q 工具名未排序: %v", d.Tier, d.Tools)
		}
		for _, name := range d.Tools {
			if seen[name] {
				t.Errorf("工具 %q 出现在多个档位", name)
			}
			seen[name] = true
			if c := ClassifyToolTimeout(name); c != d.Tier {
				t.Errorf("ClassifyToolTimeout(%q) = %q, want 描述档位 %q", name, c, d.Tier)
			}
		}
	}
	for name := range timeoutTierTools {
		if !seen[name] {
			t.Errorf("已知工具 %q 未出现在描述中", name)
		}
	}
}

// TestDescribeTimeoutsDeterministicAndPure 锁定 DescribeTimeouts 的确定性、
// 结果与内部状态隔离（修改返回值不影响后续调用），以及并发只读安全。
func TestDescribeTimeoutsDeterministicAndPure(t *testing.T) {
	want := DescribeTimeouts()

	// 重复调用结果必须逐字段一致。
	for rep := 0; rep < 32; rep++ {
		if got := DescribeTimeouts(); !reflect.DeepEqual(got, want) {
			t.Fatalf("rep %d: 结果漂移: %+v, want %+v", rep, got, want)
		}
	}

	// 修改返回值（含其内部的 Tools 切片）不得污染后续调用。
	mutated := DescribeTimeouts()
	mutated[0].Tier = "hacked"
	mutated[0].Seconds = -1
	mutated[0].Tools[0] = "hacked_tool"
	if got := DescribeTimeouts(); !reflect.DeepEqual(got, want) {
		t.Fatalf("修改返回值后结果被污染: %+v, want %+v", got, want)
	}

	// 并发只读：所有 goroutine 都应看到同一份结果。
	r40RunConcurrently(16, func(_ int) {
		if got := DescribeTimeouts(); !reflect.DeepEqual(got, want) {
			t.Errorf("并发结果不一致: %+v, want %+v", got, want)
		}
	})
}

// TestTimeoutTierFunctionsDeterministic 验证 TimeoutForTier 与
// ClassifyToolTimeout 在重复及并发调用下结果一致（无可变共享状态）。
func TestTimeoutTierFunctionsDeterministic(t *testing.T) {
	tiers := []TimeoutTier{TimeoutFast, TimeoutNormal, TimeoutLong, TimeoutBatch, TimeoutTier("unknown")}
	tools := []string{"bash", "edit", "web_fetch", "batch_files", "未知工具", ""}

	for _, tier := range tiers {
		want := TimeoutForTier(tier)
		for rep := 0; rep < 16; rep++ {
			if got := TimeoutForTier(tier); got != want {
				t.Fatalf("TimeoutForTier(%q) rep %d 漂移", tier, rep)
			}
		}
		r40RunConcurrently(8, func(_ int) {
			if got := TimeoutForTier(tier); got != want {
				t.Errorf("TimeoutForTier(%q) 并发结果漂移", tier)
			}
		})
	}
	for _, name := range tools {
		want := ClassifyToolTimeout(name)
		for rep := 0; rep < 16; rep++ {
			if got := ClassifyToolTimeout(name); got != want {
				t.Fatalf("ClassifyToolTimeout(%q) rep %d 漂移", name, rep)
			}
		}
		r40RunConcurrently(8, func(_ int) {
			if got := ClassifyToolTimeout(name); got != want {
				t.Errorf("ClassifyToolTimeout(%q) 并发结果漂移", name)
			}
		})
	}
}
