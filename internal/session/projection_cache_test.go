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
	cache := ProjectionCache{Incremental: true}
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

// TestProjectionCacheWholeModeRecomputesWithCrossRecordState 固定整段模式的安全语义：
// 跨记录状态的投影（如工具调用配对）在记录变化时整段重算，绝不拿"旧前缀 + 新尾部"
// 拼接，否则早前调用在尾部配对成功后，旧结果里的合成标记不会被撤销。
func TestProjectionCacheWholeModeRecomputesWithCrossRecordState(t *testing.T) {
	var cache ProjectionCache // 默认整段模式
	base := []Record{pcRec("a", KindUser), pcRec("b", KindAssistant)}
	cache.Project(base, projectIDs)

	projectWithPairing := func(records []Record) []Entry {
		hasA := false
		for _, record := range records {
			if record.ID == "a" {
				hasA = true
			}
		}
		out := projectIDs(records)
		if hasA && len(records) > len(base) {
			out[len(out)-1].Name = "paired"
		}
		return out
	}
	more := append(base, pcRec("c", KindUser))
	got := cache.Project(more, projectWithPairing)
	if len(got) != 3 || got[2].Name != "paired" {
		t.Fatalf("cross-record state lost: %+v", got)
	}
	// stamp 相同但 project 不同：整段模式复用上次结果，必须返回副本而非共享底层数组。
	again := cache.Project(more, projectIDs)
	if len(again) != 3 || again[2].Name != "paired" {
		t.Fatalf("cached result not reused: %+v", again)
	}
	again[2].Name = "mutated"
	if third := cache.Project(more, projectIDs); third[2].Name != "paired" {
		t.Fatal("caller mutation leaked into cached entries")
	}
}

// TestSessionProjectedEntriesConformance 固定接线不变量：会话缓存包装的输出与
// 直接调用 messageEntries 完全一致，覆盖首次、重复命中、追加、分叉与缩短。
func TestSessionProjectedEntriesConformance(t *testing.T) {
	s := &Session{}
	base := []Record{
		pcRec("a", KindUser),
		{ID: "b", Kind: KindAssistant, Data: []byte(`{"text":"hi","tool_calls":[{"id":"c1","function":{"name":"bash"}}]}`)},
		{ID: "c", Kind: KindTool, Data: []byte(`{"tool_call_id":"c1","content":"ok"}`)},
	}
	check := func(label string, records []Record) {
		t.Helper()
		got := s.projectedEntries(records)
		want := messageEntries(records)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: cached = %+v, want %+v", label, got, want)
		}
	}
	check("first", base)
	check("hit", base)

	more := append(append([]Record(nil), base...), pcRec("d", KindUser))
	check("append", more)
	check("append-hit", more)

	forked := append(append([]Record(nil), base...), pcRec("d", KindSummary))
	check("fork", forked)
	check("shrink", base)
}
