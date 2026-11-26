package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestSearchOrdersBySessionThenLineAndHonoursLimit(t *testing.T) {
	dir := t.TempDir()
	old := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	fresh := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)

	writeRawSession(t, filepath.Join(dir, "older.jsonl"), []Record{
		{ID: "n1", TS: old, Kind: KindHeader, Data: json.RawMessage(`{"title":"old title"}`)},
		{ID: "n2", Parent: "n1", TS: old, Kind: KindTool, Data: json.RawMessage(`{"output":"needle in old tool output"}`)},
	})
	writeRawSession(t, filepath.Join(dir, "newer.jsonl"), []Record{
		{ID: "n1", TS: fresh, Kind: KindHeader, Data: json.RawMessage(`{"title":"new title"}`)},
		{ID: "n2", Parent: "n1", TS: fresh, Kind: KindUser, Data: json.RawMessage(`{"text":"needle in a user message"}`)},
		{ID: "n3", Parent: "n2", TS: fresh, Kind: KindTool, Data: json.RawMessage(`{"output":"NEEDLE in new tool output"}`)},
	})

	hits, err := Search(dir, "NEEDLE", 2)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("hits = %d, want 2 (limit): %+v", len(hits), hits)
	}
	if hits[0].ID != "newer" || hits[0].Line != 2 || hits[0].Title != "new title" {
		t.Fatalf("first hit = %+v, want newest session line 2", hits[0])
	}
	if hits[1].ID != "newer" || hits[1].Line != 3 {
		t.Fatalf("second hit = %+v, want newest session line 3", hits[1])
	}
	if !strings.Contains(hits[1].Snippet, "new tool output") {
		t.Fatalf("snippet = %q", hits[1].Snippet)
	}
	if !hits[0].TS.Equal(fresh) {
		t.Fatalf("TS = %v, want %v", hits[0].TS, fresh)
	}

	all, err := Search(dir, "needle", 0)
	if err != nil {
		t.Fatalf("Search unlimited: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("unlimited hits = %d, want 3", len(all))
	}
	if all[2].ID != "older" {
		t.Fatalf("last hit = %+v, want older session", all[2])
	}
}

// JSON 对象解码为 Go map，其迭代顺序是随机的。
// 当一条记录中有多个字符串值包含 needle 时，所选择的
// 片段在多次调用间仍必须稳定：重新访问同一转录
// 不得产生关于同一文本的不同窗口。这固化了 collectStrings 中的修复
// （按排序后的顺序访问键）。
func TestSnippetForPicksDeterministicValueAcrossCalls(t *testing.T) {
	data, err := json.Marshal(map[string]any{
		"alpha": strings.Repeat("A", 80) + "NEEDLE" + strings.Repeat("B", 80),
		"beta":  strings.Repeat("C", 80) + "NEEDLE" + strings.Repeat("D", 80),
		"gamma": strings.Repeat("E", 80) + "NEEDLE" + strings.Repeat("F", 80),
		"delta": strings.Repeat("G", 80) + "NEEDLE" + strings.Repeat("H", 80),
	})
	if err != nil {
		t.Fatal(err)
	}
	want, ok := snippetFor(data, "NEEDLE")
	if !ok {
		t.Fatal("snippetFor found no hit")
	}
	// 键已排序，"alpha" 总是最先被访问，因此窗口必须
	// 始终来自它的值（开头连续的 A）。
	if !strings.Contains(want, strings.Repeat("A", 40)) {
		t.Fatalf("snippet = %q, want the window from the alphabetically-first key", want)
	}
	for i := 0; i < 200; i++ {
		got, ok := snippetFor(data, "NEEDLE")
		if !ok {
			t.Fatal("snippetFor found no hit")
		}
		if got != want {
			t.Fatalf("call %d snippet = %q, want %q (nondeterministic map order)", i, got, want)
		}
	}
}

// stringValues 把载荷的字符串提供给 snippetFor。其顺序必须只
// 取决于载荷本身，绝不取决于 Go map 的随机种子；否则
// "第一个"匹配的字符串就没有明确定义。
func TestStringValuesUsesSortedKeyOrder(t *testing.T) {
	data := json.RawMessage(`{"b":"bee","a":"aye","c":"see"}`)
	want := []string{"aye", "bee", "see"}
	for i := 0; i < 50; i++ {
		got := stringValues(data)
		if len(got) != len(want) {
			t.Fatalf("stringValues = %v, want %v", got, want)
		}
		for j := range want {
			if got[j] != want[j] {
				t.Fatalf("stringValues = %v, want %v", got, want)
			}
		}
	}
}

func TestSearchNoMatchReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	writeRawSession(t, filepath.Join(dir, "empty.jsonl"), []Record{
		{ID: "n1", Kind: KindUser, Data: json.RawMessage(`"hello"`)},
	})
	hits, err := Search(dir, "absent", 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("hits = %+v, want none", hits)
	}
}

func TestSearchRedactsSecretInSnippet(t *testing.T) {
	dir := t.TempDir()
	writeRawSession(t, filepath.Join(dir, "secret.jsonl"), []Record{
		{ID: "n1", Kind: KindTool, Data: json.RawMessage(`{"output":"token = FAKE_SECRET_VALUE ok"}`)},
	})
	hits, err := Search(dir, "token", 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits = %+v", hits)
	}
	if strings.Contains(hits[0].Snippet, "FAKE_SECRET_VALUE") {
		t.Fatalf("secret leaked: %q", hits[0].Snippet)
	}
	if !strings.Contains(hits[0].Snippet, "[redacted:") {
		t.Fatalf("snippet not redacted: %q", hits[0].Snippet)
	}
}

// searchByFullScan 是索引出现前的 Search，用作语义基线：
// 不查询旁车文件，每份转录都从头解析。
func searchByFullScan(t *testing.T, dir, query string, limit int) []Hit {
	t.Helper()
	needle := strings.TrimSpace(query)
	if needle == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	type transcript struct {
		path    string
		id      string
		title   string
		updated time.Time
		lines   []numberedRecord
	}
	files := make([]transcript, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), fileExt) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		lines, err := readNumberedRecords(path)
		if err != nil {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), fileExt)
		records := make([]Record, 0, len(lines))
		for _, line := range lines {
			records = append(records, line.record)
		}
		meta := metaFromRecords(id, records)
		updated := meta.Updated
		if updated.IsZero() {
			if info, err := entry.Info(); err == nil {
				updated = info.ModTime()
			}
		}
		files = append(files, transcript{path: path, id: id, title: meta.Title, updated: updated, lines: lines})
	}
	sort.SliceStable(files, func(i, j int) bool {
		if files[i].updated.Equal(files[j].updated) {
			return files[i].id > files[j].id
		}
		return files[i].updated.After(files[j].updated)
	})

	hits := make([]Hit, 0)
	for _, file := range files {
		for _, line := range file.lines {
			snippet, ok := snippetFor(line.record.Data, needle)
			if !ok {
				continue
			}
			hits = append(hits, Hit{
				Path: file.path, ID: file.id, Title: file.title,
				Line: line.number, Snippet: snippet, TS: line.record.TS,
			})
			if limit > 0 && len(hits) >= limit {
				return hits
			}
		}
	}
	return hits
}

func assertHitsEqual(t *testing.T, got, want []Hit) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("hits = %d, want %d: %+v", len(got), len(want), got)
	}
	for i := range got {
		if got[i].Path != want[i].Path || got[i].ID != want[i].ID ||
			got[i].Title != want[i].Title || got[i].Line != want[i].Line ||
			got[i].Snippet != want[i].Snippet || !got[i].TS.Equal(want[i].TS) {
			t.Errorf("hit[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestSearchSidecarMatchesFullScan(t *testing.T) {
	dir := t.TempDir()
	alpha := newTestSession(t, dir, "alpha")
	appendAll(t, alpha, KindUser, KindAssistant)
	if err := alpha.Append(KindTool, map[string]string{"output": "NEEDLE in tool output"}); err != nil {
		t.Fatal(err)
	}
	beta := newTestSession(t, dir, "beta")
	if err := beta.Append(KindUser, "needle in beta"); err != nil {
		t.Fatal(err)
	}
	writeRawSession(t, filepath.Join(dir, "raw.jsonl"), []Record{
		{ID: "n1", TS: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), Kind: KindUser, Data: json.RawMessage(`"a needle in a raw transcript"`)},
	})

	for _, tc := range []struct {
		query string
		limit int
	}{
		{"needle", 0},
		{"NEEDLE", 0},
		{"needle", 2},
		{"absent", 0},
	} {
		want := searchByFullScan(t, dir, tc.query, tc.limit)
		got, err := Search(dir, tc.query, tc.limit)
		if err != nil {
			t.Fatalf("Search(%q, %d): %v", tc.query, tc.limit, err)
		}
		assertHitsEqual(t, got, want)
	}

	// 第二遍必须使用旁车文件并返回相同的命中。
	past := pinSidecars(t, dir)
	want := searchByFullScan(t, dir, "needle", 0)
	got, err := Search(dir, "needle", 0)
	if err != nil {
		t.Fatalf("Search (cached): %v", err)
	}
	assertHitsEqual(t, got, want)
	assertSidecarsUntouched(t, dir, past)

	// 损坏的旁车文件会被忽略：正文仍被完整搜索。
	if err := os.WriteFile(alpha.path()+idxExt, []byte("broken json"), 0o644); err != nil {
		t.Fatal(err)
	}
	want = searchByFullScan(t, dir, "needle", 0)
	got, err = Search(dir, "needle", 0)
	if err != nil {
		t.Fatalf("Search (corrupt sidecar): %v", err)
	}
	assertHitsEqual(t, got, want)
	info, err := os.Stat(alpha.path())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := loadIndex(alpha.path(), info, alpha.ID()); !ok {
		t.Fatal("corrupt sidecar was not rebuilt by Search")
	}
}

func TestSearchFallsBackWhenSidecarIsStale(t *testing.T) {
	dir := t.TempDir()
	s := newTestSession(t, dir, "task")
	if err := s.Append(KindUser, "before"); err != nil {
		t.Fatal(err)
	}
	if _, err := Search(dir, "needle", 0); err != nil {
		t.Fatal(err)
	}
	record := Record{
		ID: "n000003", Parent: "n000002",
		TS:   time.Date(2026, 7, 8, 9, 10, 11, 0, time.UTC),
		Kind: KindTool, Data: json.RawMessage(`{"output":"a new needle"}`),
	}
	line, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(s.path(), os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(append(line, '\n')); err != nil {
		file.Close()
		t.Fatal(err)
	}
	file.Close()

	want := searchByFullScan(t, dir, "needle", 0)
	got, err := Search(dir, "needle", 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	assertHitsEqual(t, got, want)
	if len(got) != 1 || got[0].Line != 3 {
		t.Fatalf("hits = %+v, want the externally appended line 3", got)
	}
}

func TestStoreSearchDelegates(t *testing.T) {
	dir := t.TempDir()
	writeRawSession(t, filepath.Join(dir, "s.jsonl"), []Record{
		{ID: "n1", Kind: KindUser, Data: json.RawMessage(`"find me"`)},
	})
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	hits, err := store.Search("FIND", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ID != "s" {
		t.Fatalf("hits = %+v", hits)
	}
}
