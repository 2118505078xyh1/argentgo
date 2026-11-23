package tools

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestProbeLimitAndFinalize(t *testing.T) {
	if got := probeLimit(maxMatches); got != maxMatches+1 {
		t.Fatalf("probeLimit = %d，期望 %d", got, maxMatches+1)
	}
	if got := probeLimit(0); got != 0 {
		t.Fatalf("probeLimit(0) = %d", got)
	}
	o := (searchOutcome{}).finalize(maxMatches+1, maxMatches)
	if !o.Truncated || o.Limit != maxMatches {
		t.Fatalf("finalize = %+v", o)
	}
	if o2 := (searchOutcome{}).finalize(3, maxMatches); o2.Truncated {
		t.Fatalf("未超上限却标记截断：%+v", o2)
	}
}

func TestSearchNoteSuffixAndLineCountCompatibility(t *testing.T) {
	outcome := searchOutcome{Truncated: true, Limit: maxMatches, RegexHint: "lookaround 已按字面量搜索"}
	got := searchNoteSuffix(outcome, context.Background())
	if !strings.Contains(got, "truncated") || !strings.Contains(got, "字面量") {
		t.Fatalf("尾注 = %q", got)
	}
	body := strings.Repeat("hit\n", maxMatches)
	withNote := appendSearchNote(body, outcome, context.Background())
	if n := strings.Count(withNote, "\n"); n != strings.Count(body, "\n") {
		t.Fatalf("附加提示改变了行数：%d vs %d", n, strings.Count(body, "\n"))
	}
	if !strings.Contains(strings.Split(withNote, "\n")[maxMatches-1], "truncated") {
		t.Fatal("截断提示应落在最后一条结果行上")
	}
}

func TestSearchTimeoutHintChinese(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	hint := searchTimeoutHint(ctx)
	if !strings.Contains(hint, "缩小") || !strings.Contains(hint, "超时") {
		t.Fatalf("超时建议 = %q", hint)
	}
	if got := searchTimeoutHint(context.Background()); got != "" {
		t.Fatalf("未超时不应有建议：%q", got)
	}

	// 超时 + 截断同时出现时，两种提示都要保留。
	out := appendSearchNote("line", searchOutcome{Truncated: true, Limit: 1}, ctx)
	if !strings.Contains(out, "truncated") || !strings.Contains(out, "超时") {
		t.Fatalf("合并尾注 = %q", out)
	}
}
