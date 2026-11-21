package session

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// snapshotRawSummary 模拟 Summarize 的原始输出：Goal 有真实内容，且带有工具活动段。
func snapshotRawSummary() string {
	return "## Compaction\n" +
		"- dropped 6 message(s), kept the last 2\n\n" +
		"## Goal\n- 构建词法分析器\n- 修复解析错误\n\n" +
		"## Files changed\n- lexer.go\n- parser.go\n\n" +
		"## Tool activity\n- read lexer.go:10-20\n- bash go test ./...\n"
}

// snapshotRebuiltSummary 模拟 buildCompactionSummary 的输出：Goal 硬编码为 (none)，且无工具活动段。
func snapshotRebuiltSummary() string {
	return "## Compaction\n" +
		"- dropped 6 message(s), kept the last 2\n\n" +
		"## Goal\n- (none)\n\n" +
		"## Files changed\n- lexer.go\n- parser.go\n"
}

const (
	snapshotGoalBlock     = "## Goal\n- 构建词法分析器\n- 修复解析错误\n"
	snapshotActivityBlock = "## Tool activity\n- read lexer.go:10-20\n- bash go test ./...\n"
)

// TestSnapshotBeforeRebuildCapturesFields 验证含要素时所有字段都被正确捕获。
func TestSnapshotBeforeRebuildCapturesFields(t *testing.T) {
	compaction := Compaction{
		Summary:   snapshotRawSummary(),
		Dropped:   6,
		Keep:      2,
		Files:     []string{"lexer.go", "parser.go"},
		Failures:  []string{"compile: undefined: token"},
		NextSteps: []string{"补充单元测试"},
	}
	got := SnapshotBeforeRebuild(compaction)
	want := CompactionSnapshot{
		OriginalSummary: compaction.Summary,
		DroppedCount:    6,
		KeptCount:       2,
		HasGoal:         true,
		HasActivity:     true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SnapshotBeforeRebuild() = %#v, want %#v", got, want)
	}
}

// TestSnapshotBeforeRebuildMissingSections 验证缺要素时标记为 false。
func TestSnapshotBeforeRebuildMissingSections(t *testing.T) {
	tests := []struct {
		name    string
		summary string
		want    CompactionSnapshot
	}{
		{
			name:    "only goal",
			summary: "## Compaction\n- x\n\n## Goal\n- 目标\n",
			want:    CompactionSnapshot{HasGoal: true},
		},
		{
			name:    "only tool activity",
			summary: "## Compaction\n- x\n\n## Tool activity\n- read a.go\n",
			want:    CompactionSnapshot{HasActivity: true},
		},
		{
			name:    "neither section",
			summary: "## Compaction\n- x\n\n## Files changed\n- a.go\n",
			want:    CompactionSnapshot{},
		},
		{
			name:    "empty summary",
			summary: "",
			want:    CompactionSnapshot{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			compaction := Compaction{Summary: tt.summary, Dropped: 3, Keep: 1}
			got := SnapshotBeforeRebuild(compaction)
			want := tt.want
			want.OriginalSummary = tt.summary
			want.DroppedCount = 3
			want.KeptCount = 1
			if !reflect.DeepEqual(got, want) {
				t.Errorf("SnapshotBeforeRebuild() = %#v, want %#v", got, want)
			}
		})
	}
}

// TestSnapshotBeforeRebuildCJK 验证 CJK 内容被逐字捕获且 UTF-8 合法。
func TestSnapshotBeforeRebuildCJK(t *testing.T) {
	summary := "## 目标\n- 实现中文摘要\n- 保持原样\n\n" +
		"## 工具活动\n- 读取 文件.go:3-5\n- 编辑 文件.go\n"
	got := SnapshotBeforeRebuild(Compaction{Summary: summary})
	if got.OriginalSummary != summary {
		t.Errorf("OriginalSummary = %q, want %q", got.OriginalSummary, summary)
	}
	if !got.HasGoal || !got.HasActivity {
		t.Errorf("HasGoal = %v, HasActivity = %v, want true/true", got.HasGoal, got.HasActivity)
	}
	if !utf8.ValidString(got.OriginalSummary) {
		t.Error("OriginalSummary 不是合法的 UTF-8，可能被按字节截断")
	}
	if !strings.Contains(got.OriginalSummary, "实现中文摘要") ||
		!strings.Contains(got.OriginalSummary, "读取 文件.go:3-5") {
		t.Errorf("CJK 内容丢失: %q", got.OriginalSummary)
	}
}

// TestCompactionSnapshotRestoreRoundTrip 验证重建前后往返能恢复 Goal 与 Tool activity。
func TestCompactionSnapshotRestoreRoundTrip(t *testing.T) {
	snapshot := SnapshotBeforeRebuild(Compaction{
		Summary: snapshotRawSummary(),
		Dropped: 6,
		Keep:    2,
	})
	restored := snapshot.Restore(snapshotRebuiltSummary())

	if !strings.Contains(restored, snapshotGoalBlock) {
		t.Errorf("恢复结果缺少原始 Goal 段:\n%q", restored)
	}
	if !strings.Contains(restored, snapshotActivityBlock) {
		t.Errorf("恢复结果缺少原始 Tool activity 段:\n%q", restored)
	}
	if strings.Contains(restored, "## Goal\n- (none)\n") {
		t.Error("重建的 Goal=(none) 未被原始内容替换")
	}
	if snapshot.DroppedCount != 6 || snapshot.KeptCount != 2 {
		t.Errorf("计数不符: DroppedCount=%d, KeptCount=%d", snapshot.DroppedCount, snapshot.KeptCount)
	}
}

// TestCompactionSnapshotRestoreOnlySections 验证只有单一要素时的恢复行为。
func TestCompactionSnapshotRestoreOnlySections(t *testing.T) {
	t.Run("only goal", func(t *testing.T) {
		orig := "## Goal\n- 唯一目标\n"
		snapshot := SnapshotBeforeRebuild(Compaction{Summary: orig})
		restored := snapshot.Restore(snapshotRebuiltSummary())
		if !strings.Contains(restored, "## Goal\n- 唯一目标\n") {
			t.Errorf("未恢复 Goal:\n%q", restored)
		}
		if strings.Contains(restored, "## Tool activity") {
			t.Error("orig 无工具活动段时不应追加该段")
		}
	})

	t.Run("only activity", func(t *testing.T) {
		orig := "## Tool activity\n- read only.go\n"
		snapshot := SnapshotBeforeRebuild(Compaction{Summary: orig})
		rebuilt := snapshotRebuiltSummary()
		restored := snapshot.Restore(rebuilt)
		if !strings.Contains(restored, "## Tool activity\n- read only.go\n") {
			t.Errorf("未恢复 Tool activity:\n%q", restored)
		}
		if !strings.Contains(restored, "## Goal\n- (none)\n") {
			t.Error("orig 无 Goal 段时不应改动 rebuilt 的 Goal 段")
		}
	})
}

// TestCompactionSnapshotRestoreMissingSections 验证缺要素时 Restore 原样返回 rebuilt。
func TestCompactionSnapshotRestoreMissingSections(t *testing.T) {
	rebuilt := snapshotRebuiltSummary()
	for _, summary := range []string{"", "## Compaction\n- x\n", "## Files changed\n- a.go\n"} {
		snapshot := SnapshotBeforeRebuild(Compaction{Summary: summary})
		if got := snapshot.Restore(rebuilt); got != rebuilt {
			t.Errorf("Restore() = %q, want rebuilt 原样返回", got)
		}
	}
}

// TestCompactionSnapshotRestoreIdempotent 验证 Restore 幂等。
func TestCompactionSnapshotRestoreIdempotent(t *testing.T) {
	tests := []struct {
		name    string
		summary string
		rebuilt string
	}{
		{name: "both sections", summary: snapshotRawSummary(), rebuilt: snapshotRebuiltSummary()},
		{name: "only goal", summary: "## Goal\n- 唯一目标\n", rebuilt: snapshotRebuiltSummary()},
		{name: "only activity", summary: "## Tool activity\n- read a.go\n", rebuilt: snapshotRebuiltSummary()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := SnapshotBeforeRebuild(Compaction{Summary: tt.summary})
			once := snapshot.Restore(tt.rebuilt)
			twice := snapshot.Restore(once)
			if once != twice {
				t.Errorf("非幂等:\nonce =\n%q\ntwice =\n%q", once, twice)
			}
		})
	}
}

// TestSnapshotBeforeRebuildDeterministic 验证两个原语均稳定（相同入参得到相同结果）。
func TestSnapshotBeforeRebuildDeterministic(t *testing.T) {
	compaction := Compaction{Summary: snapshotRawSummary(), Dropped: 7, Keep: 3}
	wantSnapshot := SnapshotBeforeRebuild(compaction)
	wantRestored := wantSnapshot.Restore(snapshotRebuiltSummary())
	for i := 0; i < 50; i++ {
		if got := SnapshotBeforeRebuild(compaction); !reflect.DeepEqual(got, wantSnapshot) {
			t.Fatalf("第 %d 次 SnapshotBeforeRebuild 结果不稳定", i)
		}
		if got := wantSnapshot.Restore(snapshotRebuiltSummary()); got != wantRestored {
			t.Fatalf("第 %d 次 Restore 结果不稳定", i)
		}
	}
}

// TestSnapshotBeforeRebuildDoesNotMutateInputs 验证两个原语都不改动入参。
func TestSnapshotBeforeRebuildDoesNotMutateInputs(t *testing.T) {
	compaction := Compaction{
		Summary:   snapshotRawSummary(),
		Dropped:   6,
		Keep:      2,
		Files:     []string{"lexer.go", "parser.go"},
		Failures:  []string{"compile: undefined: token"},
		NextSteps: []string{"补充单元测试"},
	}
	compactionCopy := Compaction{
		Summary:   compaction.Summary,
		Dropped:   compaction.Dropped,
		Keep:      compaction.Keep,
		Files:     append([]string(nil), compaction.Files...),
		Failures:  append([]string(nil), compaction.Failures...),
		NextSteps: append([]string(nil), compaction.NextSteps...),
	}
	rebuilt := snapshotRebuiltSummary()
	rebuiltCopy := rebuilt

	snapshot := SnapshotBeforeRebuild(compaction)
	_ = snapshot.Restore(rebuilt)

	if !reflect.DeepEqual(compaction, compactionCopy) {
		t.Errorf("SnapshotBeforeRebuild 改动了入参: %#v", compaction)
	}
	if rebuilt != rebuiltCopy {
		t.Errorf("Restore 改动了 rebuilt: %q", rebuilt)
	}
	// 后续对快照字段的改动不应反向影响原始摘要（Go 字符串按值持有）。
	snapshot.OriginalSummary = "changed"
	if compaction.Summary != compactionCopy.Summary {
		t.Error("改动快照字段影响了原始 Compaction.Summary")
	}
}

// TestCompactionSnapshotZeroValue 验证零值行为：空快照的 Restore 原样返回 rebuilt。
func TestCompactionSnapshotZeroValue(t *testing.T) {
	var snapshot CompactionSnapshot
	if snapshot.OriginalSummary != "" || snapshot.DroppedCount != 0 || snapshot.KeptCount != 0 ||
		snapshot.HasGoal || snapshot.HasActivity {
		t.Errorf("零值快照字段非零: %#v", snapshot)
	}
	rebuilt := snapshotRebuiltSummary()
	if got := snapshot.Restore(rebuilt); got != rebuilt {
		t.Errorf("零值 Restore() = %q, want rebuilt 原样返回", got)
	}

	// 空 Compaction 也应得到除 OriginalSummary 外全为零的快照。
	empty := SnapshotBeforeRebuild(Compaction{})
	if empty.OriginalSummary != "" || empty.HasGoal || empty.HasActivity {
		t.Errorf("空 Compaction 快照异常: %#v", empty)
	}
}
