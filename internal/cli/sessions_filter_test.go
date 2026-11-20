package cli

import (
	"strings"
	"testing"
	"time"
)

// sessionTestTS 构造 UTC 时间戳，便于在测试中书写命中数据。
func sessionTestTS(year int, month time.Month, day, hour, min, sec int) time.Time {
	return time.Date(year, month, day, hour, min, sec, 0, time.UTC)
}

// sessionTestHits 返回一组覆盖多标题、多目录、多时间与多摘要的命中。
func sessionTestHits() []SessionHit {
	return []SessionHit{
		{ID: "s1", Title: "设计文档", Path: "/work/proj/sessions/s1.jsonl", Snippet: "needle alpha", TS: sessionTestTS(2026, time.January, 1, 10, 0, 0)},
		{ID: "s2", Title: "设计评审", Path: "/work/other/s2.jsonl", Snippet: "needle beta", TS: sessionTestTS(2026, time.January, 2, 10, 0, 0)},
		{ID: "s3", Title: "开发笔记", Path: "/work/proj/sessions/s3.jsonl", Snippet: "gamma", TS: sessionTestTS(2026, time.January, 3, 10, 0, 0)},
	}
}

// sessionHitIDs 抽取命中的 ID，方便断言过滤结果与顺序。
func sessionHitIDs(hits []SessionHit) []string {
	ids := make([]string, 0, len(hits))
	for _, hit := range hits {
		ids = append(ids, hit.ID)
	}
	return ids
}

// assertHitIDs 比较 ID 序列，顺序敏感。
func assertHitIDs(t *testing.T, got []SessionHit, want ...string) {
	t.Helper()
	ids := sessionHitIDs(got)
	if len(ids) != len(want) {
		t.Fatalf("ids = %v, want %v (%+v)", ids, want, got)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("ids = %v, want %v (%+v)", ids, want, got)
		}
	}
}

func TestFilterSessionHitsByTitleIsCaseInsensitiveSubstring(t *testing.T) {
	got := filterSessionHits(sessionTestHits(), SessionFilter{Title: "设计"})
	assertHitIDs(t, got, "s2", "s1")
	// 大小写不敏感：英文标题片段。
	ascii := []SessionHit{{ID: "x", Title: "Design Notes"}, {ID: "y", Title: "Other"}}
	assertHitIDs(t, filterSessionHits(ascii, SessionFilter{Title: "design"}), "x")
}

func TestFilterSessionHitsByTimeRangeIsInclusive(t *testing.T) {
	hits := sessionTestHits()
	got := filterSessionHits(hits, SessionFilter{
		Since: "2026-01-02",
		Until: "2026-01-03T10:00:00Z",
	})
	// 下界包含 1 月 2 日 00:00 之后的 s2；上界恰好等于 s3 的时间戳。
	assertHitIDs(t, got, "s3", "s2")
}

func TestFilterSessionHitsByDirectory(t *testing.T) {
	hits := sessionTestHits()
	assertHitIDs(t, filterSessionHits(hits, SessionFilter{In: "sessions"}), "s3", "s1")
	assertHitIDs(t, filterSessionHits(hits, SessionFilter{In: "other"}), "s2")
	// 分隔符归一化：反斜杠写法也能匹配 POSIX 路径。
	assertHitIDs(t, filterSessionHits(hits, SessionFilter{In: `proj\sessions`}), "s3", "s1")
}

func TestFilterSessionHitsByTextSearchesFields(t *testing.T) {
	hits := sessionTestHits()
	assertHitIDs(t, filterSessionHits(hits, SessionFilter{Text: "NEEDLE"}), "s2", "s1")
	assertHitIDs(t, filterSessionHits(hits, SessionFilter{Text: "gamma"}), "s3")
	// Text 也覆盖 ID 与标题字段。
	assertHitIDs(t, filterSessionHits(hits, SessionFilter{Text: "开发"}), "s3")
}

func TestFilterSessionHitsCombinesDimensions(t *testing.T) {
	hits := sessionTestHits()
	got := filterSessionHits(hits, SessionFilter{
		Title: "设计",
		In:    "sessions",
		Text:  "needle",
		Since: "2026-01-01",
		Until: "2026-01-02",
	})
	// 只有 s1 同时满足标题、目录、文本与时间窗。
	assertHitIDs(t, got, "s1")
}

func TestFilterSessionHitsCJKTimeRange(t *testing.T) {
	hits := sessionTestHits()
	got := filterSessionHits(hits, SessionFilter{
		Since: "2026年01月02日",
		Until: "2026年01月02日 23时59分59秒",
	})
	assertHitIDs(t, got, "s2")
}

func TestFilterSessionHitsEmptyResult(t *testing.T) {
	got := filterSessionHits(sessionTestHits(), SessionFilter{Title: "不存在的标题"})
	if len(got) != 0 {
		t.Fatalf("got %+v, want empty", got)
	}
	// 时间窗与维度互相矛盾时同样为空。
	got = filterSessionHits(sessionTestHits(), SessionFilter{Since: "2026-01-03", Until: "2026-01-01"})
	if len(got) != 0 {
		t.Fatalf("got %+v, want empty", got)
	}
}

func TestFilterSessionHitsNoFilterUnchanged(t *testing.T) {
	hits := sessionTestHits()
	got := filterSessionHits(hits, SessionFilter{})
	if len(got) != len(hits) {
		t.Fatalf("len = %d, want %d", len(got), len(hits))
	}
	for i := range hits {
		if got[i] != hits[i] {
			t.Fatalf("got[%d] = %+v, want %+v", i, got[i], hits[i])
		}
	}
	// 全空白字段同样视为无过滤。
	blank := SessionFilter{Title: "  ", Since: " ", Until: "\t", In: " ", Text: " "}
	if got := filterSessionHits(hits, blank); len(got) != len(hits) || &got[0] != &hits[0] {
		t.Fatalf("blank filter changed hits: %+v", got)
	}
}

func TestFilterSessionHitsStableSortAndNoMutation(t *testing.T) {
	// 打乱顺序的输入，过滤后按时间从新到旧稳定排序。
	hits := []SessionHit{
		{ID: "s2", Title: "keep", Path: "/a/s2.jsonl", TS: sessionTestTS(2026, time.January, 2, 0, 0, 0)},
		{ID: "s1", Title: "keep", Path: "/a/s1.jsonl", TS: sessionTestTS(2026, time.January, 1, 0, 0, 0)},
		{ID: "s3", Title: "keep", Path: "/a/s3.jsonl", TS: sessionTestTS(2026, time.January, 3, 0, 0, 0)},
		{ID: "drop", Title: "skip", Path: "/a/drop.jsonl", TS: sessionTestTS(2026, time.January, 4, 0, 0, 0)},
	}
	got := filterSessionHits(hits, SessionFilter{Title: "keep"})
	assertHitIDs(t, got, "s3", "s2", "s1")
	// 输入切片未被重排。
	if hits[0].ID != "s2" || hits[2].ID != "s3" {
		t.Fatalf("input was mutated: %+v", hits)
	}
}

func TestFilterSessionHitsSortsTiesDeterministically(t *testing.T) {
	stamp := sessionTestTS(2026, time.January, 1, 0, 0, 0)
	hits := []SessionHit{
		{ID: "b", Path: "/a/b.jsonl", Line: 2, TS: stamp},
		{ID: "a", Path: "/a/a.jsonl", Line: 3, TS: stamp},
		{ID: "a", Path: "/a/a.jsonl", Line: 1, TS: stamp},
	}
	got := filterSessionHits(hits, SessionFilter{Text: "a"})
	// 时间相同则按 ID、再按行号升序。
	if len(got) != 3 || got[0].ID != "a" || got[0].Line != 1 || got[1].Line != 3 || got[2].ID != "b" {
		t.Fatalf("got %+v", got)
	}
}

func TestSessionsGrepAppliesMultiConditionFlags(t *testing.T) {
	storage := t.TempDir()
	writeJSONLSession(t, storage, "alpha", []map[string]any{
		record("n1", "", "2026-01-01T10:00:00Z", "header", map[string]any{"title": "设计文档"}),
		record("n2", "n1", "2026-01-01T10:00:01Z", "tool", map[string]any{"output": "needle alpha"}),
	})
	writeJSONLSession(t, storage, "beta", []map[string]any{
		record("n1", "", "2026-02-01T10:00:00Z", "header", map[string]any{"title": "开发笔记"}),
		record("n2", "n1", "2026-02-01T10:00:01Z", "tool", map[string]any{"output": "needle beta"}),
	})

	cases := []struct {
		name   string
		args   []string
		wanted string
		absent string
	}{
		{"无新 flag 输出两条", []string{"sessions", "--grep", "needle"}, "beta:", "不存在的串"},
		{"标题过滤", []string{"sessions", "--grep", "needle", "--title", "设计"}, "alpha:", "beta:"},
		{"时间过滤 CJK", []string{"sessions", "--grep", "needle", "--since=2026年02月01日"}, "beta:", "alpha:"},
		{"文本过滤", []string{"sessions", "--grep", "needle", "--text", "alpha"}, "alpha:", "beta:"},
		{"目录 flag 被接受", []string{"sessions", "--grep", "needle", "--in", "sessions"}, "alpha:", "不存在的串"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdio, out, errOut := newTestIO("", nil)
			if code := RunWith(App{IO: stdio, StorageDir: storage}, tc.args); code != 0 {
				t.Fatalf("exit = %d, err %q", code, errOut.String())
			}
			got := out.String()
			if tc.wanted != "" && !strings.Contains(got, tc.wanted) {
				t.Fatalf("out = %q, want %q", got, tc.wanted)
			}
			if tc.absent != "" && strings.Contains(got, tc.absent) {
				t.Fatalf("out = %q, unexpected %q", got, tc.absent)
			}
		})
	}
}

func TestParseGrepArgsRejectsMissingFilterValue(t *testing.T) {
	if _, _, _, err := parseGrepArgs([]string{"needle", "--since"}); err == nil ||
		!strings.Contains(err.Error(), "--since needs a value") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseGrepArgsKeepsExistingLimitForms(t *testing.T) {
	for _, args := range [][]string{
		{"needle", "--limit", "3"},
		{"needle", "--limit=3"},
	} {
		query, limit, filter, err := parseGrepArgs(args)
		if err != nil || query != "needle" || limit != 3 || filter.active() {
			t.Fatalf("args %v -> %q/%d/%+v/%v", args, query, limit, filter, err)
		}
	}
}
