package provider_test

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/provider"
)

// cloneDiffs 复制差异切片并保留 nil 与空切片的区分，用于入参不变性断言。
func cloneDiffs(diffs []provider.OverlayDiff) []provider.OverlayDiff {
	if diffs == nil {
		return nil
	}
	out := make([]provider.OverlayDiff, len(diffs))
	copy(out, diffs)
	return out
}

func TestOverlayAuditRecordLast(t *testing.T) {
	sample := []provider.OverlayDiff{
		{Key: "b", Base: "1", Over: "2", Kind: provider.DiffKindChanged},
		{Key: "a", Over: "x", Kind: provider.DiffKindAdded},
		{Key: "中文", Base: "旧", Kind: provider.DiffKindRemoved},
	}
	tests := []struct {
		name      string
		records   map[string][]provider.OverlayDiff
		query     string
		want      []provider.OverlayDiff
		wantFound bool
	}{
		{
			name:      "查询不存在的层名返回 false",
			records:   nil,
			query:     "missing",
			want:      nil,
			wantFound: false,
		},
		{
			name:      "记录后按原名取回",
			records:   map[string][]provider.OverlayDiff{"base": sample},
			query:     "base",
			want:      sample,
			wantFound: true,
		},
		{
			name:      "CJK 层名",
			records:   map[string][]provider.OverlayDiff{"中文层": sample},
			query:     "中文层",
			want:      sample,
			wantFound: true,
		},
		{
			name:      "nil 差异被记录并保留 nil",
			records:   map[string][]provider.OverlayDiff{"empty": nil},
			query:     "empty",
			want:      nil,
			wantFound: true,
		},
		{
			name:      "空切片差异被记录并保留非 nil 空切片",
			records:   map[string][]provider.OverlayDiff{"empty": {}},
			query:     "empty",
			want:      []provider.OverlayDiff{},
			wantFound: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var audit provider.OverlayAudit
			for name, diffs := range tt.records {
				audit.Record(name, diffs)
			}
			got, found := audit.Last(tt.query)
			if found != tt.wantFound {
				t.Fatalf("Last(%q) found = %v, want %v", tt.query, found, tt.wantFound)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Last(%q) = %#v, want %#v", tt.query, got, tt.want)
			}
		})
	}
}

func TestOverlayAuditLastReturnsLatestRecord(t *testing.T) {
	var audit provider.OverlayAudit
	first := []provider.OverlayDiff{{Key: "a", Over: "1", Kind: provider.DiffKindAdded}}
	second := []provider.OverlayDiff{{Key: "b", Base: "2", Kind: provider.DiffKindRemoved}}

	audit.Record("layer", first)
	audit.Record("layer", second)

	got, found := audit.Last("layer")
	if !found {
		t.Fatal("Last(layer) found = false, want true")
	}
	if !reflect.DeepEqual(got, second) {
		t.Fatalf("Last(layer) = %#v, want 最新记录 %#v", got, second)
	}
}

func TestOverlayAuditSummary(t *testing.T) {
	added := []provider.OverlayDiff{{Key: "模型", Over: "x", Kind: provider.DiffKindAdded}}
	mixed := []provider.OverlayDiff{
		{Key: "a", Over: "1", Kind: provider.DiffKindAdded},
		{Key: "b", Base: "1", Over: "2", Kind: provider.DiffKindChanged},
		{Key: "中文", Base: "旧", Kind: provider.DiffKindRemoved},
	}
	tests := []struct {
		name    string
		records map[string][]provider.OverlayDiff
		want    string
	}{
		{
			name:    "零记录返回固定字面量",
			records: nil,
			want:    "无覆盖审计记录",
		},
		{
			name:    "单条记录复用 SummarizeDiffs",
			records: map[string][]provider.OverlayDiff{"base": added},
			want:    "base：共 1 项变化：新增 1 项，删除 0 项，变更 0 项",
		},
		{
			name:    "空差异记录输出无差异",
			records: map[string][]provider.OverlayDiff{"empty": {}},
			want:    "empty：无差异",
		},
		{
			name: "多条记录按层名字典序稳定排序并覆盖 CJK",
			records: map[string][]provider.OverlayDiff{
				"温度":    added,
				"alpha": mixed,
				"中文":    nil,
			},
			// ASCII 'a'(0x61) 排在 CJK 之前；"中"(E4 B8 AD) < "温"(E6 B8 A9)。
			want: strings.Join([]string{
				"alpha：共 3 项变化：新增 1 项，删除 1 项，变更 1 项",
				"中文：无差异",
				"温度：共 1 项变化：新增 1 项，删除 0 项，变更 0 项",
			}, "\n"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var audit provider.OverlayAudit
			for name, diffs := range tt.records {
				audit.Record(name, diffs)
			}
			if got := audit.Summary(); got != tt.want {
				t.Fatalf("Summary() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOverlayAuditSummaryStableAcrossRepeatedCalls(t *testing.T) {
	var audit provider.OverlayAudit
	// 反复以不同顺序写入相同的层，汇总结果不应受记录顺序影响。
	names := []string{"温度", "alpha", "中文", "beta"}
	for i := 0; i < 50; i++ {
		for _, name := range names {
			audit.Record(name, []provider.OverlayDiff{{Key: name, Over: "v", Kind: provider.DiffKindAdded}})
		}
	}
	want := strings.Join([]string{
		"alpha：共 1 项变化：新增 1 项，删除 0 项，变更 0 项",
		"beta：共 1 项变化：新增 1 项，删除 0 项，变更 0 项",
		"中文：共 1 项变化：新增 1 项，删除 0 项，变更 0 项",
		"温度：共 1 项变化：新增 1 项，删除 0 项，变更 0 项",
	}, "\n")
	for i := 0; i < 50; i++ {
		if got := audit.Summary(); got != want {
			t.Fatalf("第 %d 次 Summary() = %q, want %q", i, got, want)
		}
	}
}

func TestOverlayAuditZeroValue(t *testing.T) {
	var audit provider.OverlayAudit
	if got := audit.Summary(); got != "无覆盖审计记录" {
		t.Fatalf("零值 Summary() = %q, want 无覆盖审计记录", got)
	}
	if _, found := audit.Last("any"); found {
		t.Fatal("零值 Last(any) found = true, want false")
	}
	// 零值在 Record 后应能正常工作。
	audit.Record("layer", []provider.OverlayDiff{{Key: "k", Over: "v", Kind: provider.DiffKindAdded}})
	if got, found := audit.Last("layer"); !found || len(got) != 1 || got[0].Key != "k" {
		t.Fatalf("Record 后 Last(layer) = %#v, found=%v", got, found)
	}
}

func TestOverlayAuditDoesNotMutateInput(t *testing.T) {
	var audit provider.OverlayAudit
	diffs := []provider.OverlayDiff{
		{Key: "a", Base: "1", Over: "2", Kind: provider.DiffKindChanged},
		{Key: "b", Over: "x", Kind: provider.DiffKindAdded},
	}
	original := cloneDiffs(diffs)

	audit.Record("layer", diffs)

	// 改动入参不应影响已记录的副本。
	diffs[0].Base = "mutated"
	diffs[1].Key = "mutated"
	got, found := audit.Last("layer")
	if !found || !reflect.DeepEqual(got, original) {
		t.Fatalf("改动入参影响了记录: Last = %#v, want %#v", got, original)
	}

	// 改动 Last 的返回值也不应影响内部记录。
	got[0].Base = "changed-return"
	got = append(got, provider.OverlayDiff{Key: "extra"})
	got2, found := audit.Last("layer")
	if !found || !reflect.DeepEqual(got2, original) {
		t.Fatalf("改动 Last 返回值影响了内部记录: Last = %#v, want %#v", got2, original)
	}
}

func TestOverlayAuditConcurrent(t *testing.T) {
	const workers = 16
	var audit provider.OverlayAudit
	var wg sync.WaitGroup
	errs := make(chan string, workers)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("层-%02d", i)
			diff := []provider.OverlayDiff{{Key: name, Over: "v", Kind: provider.DiffKindAdded}}
			for j := 0; j < 100; j++ {
				audit.Record(name, diff)
				got, found := audit.Last(name)
				if !found || len(got) != 1 || got[0].Key != name {
					errs <- fmt.Sprintf("Last(%q) 不一致: %#v, found=%v", name, got, found)
					return
				}
				// Summary 与其它 goroutine 的写入并发执行，不应 panic 或返回空。
				if audit.Summary() == "" {
					errs <- "Summary() 返回空字符串"
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	// 所有层都应出现在最终汇总中，且顺序稳定。
	var b strings.Builder
	for i := 0; i < workers; i++ {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "层-%02d：共 1 项变化：新增 1 项，删除 0 项，变更 0 项", i)
	}
	want := b.String()
	if got := audit.Summary(); got != want {
		t.Fatalf("并发写入后 Summary() = %q, want %q", got, want)
	}
}
