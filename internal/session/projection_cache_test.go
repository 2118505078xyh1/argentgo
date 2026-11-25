package session

import (
	"fmt"
	"reflect"
	"testing"
)

func pcRec(id, kind string) Record { return Record{ID: id, Kind: kind} }

// projectIDs 是测试用投影：每条记录投影成 "id:kind" 的 Entry。
func projectIDs(records []Record) []Entry {
	out := make([]Entry, 0, len(records))
	for _, r := range records {
		out = append(out, Entry{ID: r.ID, Content: fmt.Sprintf("%s:%s", r.ID, r.Kind)})
	}
	return out
}

func TestProjectionCacheIncrementalMatchesFull(t *testing.T) {
	records := []Record{pcRec("a", KindUser), pcRec("b", KindAssistant)}
	var cache ProjectionCache
	cache.Project(records, projectIDs)
	if cache.Generation() != 2 {
		t.Fatalf("generation = %d, want 2", cache.Generation())
	}

	// 追加两条：命中前缀，只投影尾部。
	more := append(records, pcRec("c", KindUser), pcRec("d", KindAssistant))
	got := cache.Project(more, projectIDs)
	want := projectIDs(more)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("incremental = %+v, want %+v", got, want)
	}
	if cache.Generation() != 4 {
		t.Fatalf("generation = %d, want 4", cache.Generation())
	}
	// 再取一次（无新增）应等价。
	if again := cache.Project(more, projectIDs); !reflect.DeepEqual(again, want) {
		t.Fatalf("repeat = %+v, want %+v", again, want)
	}
}

func TestProjectionCacheInvalidatesOnFork(t *testing.T) {
	var cache ProjectionCache
	base := []Record{pcRec("a", KindUser), pcRec("b", KindAssistant)}
	cache.Project(base, projectIDs)

	// 前缀分叉（b 的 kind 改变）：必须整段重算，不能复用。
	forked := []Record{pcRec("a", KindUser), pcRec("b", KindSummary), pcRec("c", KindUser)}
	got := cache.Project(forked, projectIDs)
	if want := projectIDs(forked); !reflect.DeepEqual(got, want) {
		t.Fatalf("fork = %+v, want %+v", got, want)
	}
}

func TestProjectionCacheInvalidatesOnShrink(t *testing.T) {
	var cache ProjectionCache
	cache.Project([]Record{pcRec("a", KindUser), pcRec("b", KindAssistant), pcRec("c", KindUser)}, projectIDs)
	// 历史缩短（压缩）：必须整段重算。
	short := []Record{pcRec("a", KindUser), pcRec("b", KindAssistant)}
	got := cache.Project(short, projectIDs)
	if want := projectIDs(short); !reflect.DeepEqual(got, want) {
		t.Fatalf("shrink = %+v, want %+v", got, want)
	}
	if cache.Generation() != 2 {
		t.Fatalf("generation = %d, want 2", cache.Generation())
	}
}

func TestProjectionCacheReset(t *testing.T) {
	var cache ProjectionCache
	records := []Record{pcRec("a", KindUser)}
	cache.Project(records, projectIDs)
	cache.Reset()
	if cache.Generation() != 0 {
		t.Fatalf("generation after reset = %d", cache.Generation())
	}
	if got := cache.Project(records, projectIDs); !reflect.DeepEqual(got, projectIDs(records)) {
		t.Fatalf("after reset = %+v", got)
	}
}

func TestPrefixLen(t *testing.T) {
	a := []recordStamp{{"a", "user"}, {"b", "assistant"}}
	b := []recordStamp{{"a", "user"}, {"b", "assistant"}, {"c", "user"}}
	if got := prefixLen(a, b); got != 2 {
		t.Fatalf("prefixLen = %d, want 2", got)
	}
	if got := prefixLen(b, a); got != 2 {
		t.Fatalf("prefixLen reversed = %d, want 2", got)
	}
	c := []recordStamp{{"a", "user"}, {"X", "assistant"}}
	if got := prefixLen(a, c); got != 1 {
		t.Fatalf("diverged prefixLen = %d, want 1", got)
	}
}

func TestPageWatermark(t *testing.T) {
	if got := (Page{}).Watermark(); got != "0@" {
		t.Fatalf("empty watermark = %q", got)
	}
	p := Page{Records: []Record{pcRec("a", KindUser), pcRec("b", KindAssistant)}}
	if got := p.Watermark(); got != "2@b" {
		t.Fatalf("watermark = %q, want 2@b", got)
	}
}
