package session

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestPreserveSectionsBloatGuard 验证 #6：合并前对 orig 两段做有界裁剪，并在命中
// 早期模板标记时按新详略级别重写一次；短章节逐字节不变，结果幂等且 CJK 安全。
func TestPreserveSectionsBloatGuard(t *testing.T) {
	t.Run("短章节逐字节不变", func(t *testing.T) {
		orig := "## Goal\n- 简短目标\n\n## Tool activity\n- read a.go\n"
		got := PreserveSections(rebuiltSummaryWithoutSections(), orig)
		want := PreserveSectionsUnboundedReference(rebuiltSummaryWithoutSections(), orig)
		if got != want {
			t.Fatalf("短章节被改动:\ngot  %q\nwant %q", got, want)
		}
	})

	t.Run("超长章节按 2048 rune 裁剪", func(t *testing.T) {
		// 单行超长（>2048 rune），裁剪必须触发且 CJK 不被切开。
		long := strings.Repeat("目标细节", 700) // 2800 rune
		orig := "## Goal\n- " + long + "\n"
		got := PreserveSections(rebuiltSummaryWithoutSections(), orig)
		if !strings.Contains(got, trimmedSectionNotice) {
			t.Fatalf("超长章节未加裁剪标记: %q", got)
		}
		if n := utf8.RuneCountInString(got); n > utf8.RuneCountInString(rebuiltSummaryWithoutSections())+preservedSectionMaxRunes+64 {
			t.Fatalf("裁剪后 rune 数=%d 超出上界", n)
		}
		if !utf8.ValidString(got) {
			t.Fatal("裁剪产生非法 UTF-8")
		}
	})

	t.Run("legacy 标记触发重写并保留新历史优先", func(t *testing.T) {
		orig := "## Tool activity\n- 正常活动\n" +
			"- [checkpoint] 逐文件快照\n- 复述 AGENTS.md 的规则文件内容\n" +
			"- read a.go\n- edit b.go\n- bash go test ./...\n" +
			"- edit #1 逐次流水\n"
		got := PreserveSections(rebuiltSummaryWithoutSections(), orig)
		if !strings.Contains(got, legacySectionNotice) {
			t.Fatalf("legacy 章节未加收敛标记: %q", got)
		}
		if strings.Contains(got, "复述的规则文件内容") || strings.Contains(got, "逐次流水") {
			t.Fatalf("过时细节未被折叠: %q", got)
		}
		if !strings.Contains(got, "- read a.go") || !strings.Contains(got, "- edit b.go") {
			t.Fatalf("有效活动行丢失: %q", got)
		}
		// rebuilt 的新历史（Failures/Next steps 段）仍完整存在，旧章节没有覆盖权威字段。
		if !strings.Contains(got, "## Next steps") {
			t.Fatal("新历史被旧章节覆盖")
		}
	})

	t.Run("幂等", func(t *testing.T) {
		orig := "## Goal\n- " + strings.Repeat("目标", 1200) + "\n" +
			"## Tool activity\n- [checkpoint] 复述\n- read a.go\n"
		once := PreserveSections(rebuiltSummaryWithoutSections(), orig)
		twice := PreserveSections(once, orig)
		if once != twice {
			t.Fatalf("非幂等:\nonce=%q\ntwice=%q", once, twice)
		}
	})
}

// PreserveSectionsUnboundedReference 是一个只做合并、不做有界裁剪的参考实现，
// 用于断言「短章节逐字节不变」——短章节下两者必须一致。
func PreserveSectionsUnboundedReference(rebuilt, orig string) string {
	s := ParseSummarySections(orig)
	if !s.HasGoal && !s.HasActivity {
		return rebuilt
	}
	out := rebuilt
	if s.HasGoal {
		out = mergeSummarySection(out, summaryGoalTitle, s.Goal)
	}
	if s.HasActivity {
		out = mergeSummarySection(out, summaryActivityTitle, s.ToolActivity)
	}
	return out
}
