package session

import (
	"strconv"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/tokenize"
)

// TestPruneLossHeader 锁定 #36：session 侧裁剪标记报告原始 token 数
// 与总行数，且旧的基础标记仍作为子串存在（幂等判断不受影响）。
func TestPruneLossHeader(t *testing.T) {
	raw := strings.Repeat("中文😀abc\n", 40)
	entries := []Entry{pruneToolEntry("c1", raw)}

	pruned, count := PruneToolResults(entries, 20)
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	want := " [original ~" + strconv.Itoa(tokenize.Estimate(raw)) + " tokens, " +
		strconv.Itoa(strings.Count(raw, "\n")+1) + " lines, kept head+tail]"
	if !strings.Contains(pruned[0].Content, want) {
		t.Fatalf("裁剪结果缺少损失头 %q；实际 %q", want, pruned[0].Content)
	}
	if !strings.Contains(pruned[0].Content, pruneMarkerToken) {
		t.Fatal("基础标记不再是子串，幂等判断会失效")
	}

	// 幂等：二次裁剪不应叠加损失头。
	second, count2 := PruneToolResults(pruned, 20)
	if count2 != 0 || second[0].Content != pruned[0].Content {
		t.Fatalf("二次裁剪不再幂等：count=%d content=%q", count2, second[0].Content)
	}
}
