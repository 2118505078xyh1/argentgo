package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// pageTestRecords 构建 n 条行长度统一的记录，使字节上限
// 能干净地映射为每页固定的记录数。Parent 故意留空：
// 分页基于文件顺序，而等长行能保持
// 页大小计算的精确性。
func pageTestRecords(n int) []Record {
	ts := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	records := make([]Record, 0, n)
	for i := 0; i < n; i++ {
		records = append(records, Record{
			ID:   fmt.Sprintf("r%06d", i+1),
			TS:   ts,
			Kind: KindUser,
			Data: json.RawMessage(`"x"`),
		})
	}
	return records
}

func pageTestLineLen(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return len(strings.SplitN(string(data), "\n", 2)[0])
}

func pageTestIDs(records []Record) []string {
	ids := make([]string, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.ID)
	}
	return ids
}

// (a) 用 Next 向前遍历必须按顺序恰好访问每条记录一次，
// 且没有任何页超过字节上限。
func TestReadPageWalksForwardWithCursor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "walk.jsonl")
	records := pageTestRecords(10)
	writeRawSession(t, path, records)
	lineLen := pageTestLineLen(t, path)
	maxBytes := 3*lineLen + 1 // 恰好容纳三条等长记录

	var got []string
	after := ""
	for pages := 0; ; pages++ {
		if pages >= 20 {
			t.Fatal("forward pagination did not terminate")
		}
		page, err := ReadPage(path, after, maxBytes, false)
		if err != nil {
			t.Fatalf("ReadPage(page %d): %v", pages, err)
		}
		if page.Bytes > maxBytes {
			t.Fatalf("page %d bytes = %d > cap %d", pages, page.Bytes, maxBytes)
		}
		if want := len(page.Records) * lineLen; page.Bytes != want {
			t.Errorf("page %d bytes = %d, want %d", pages, page.Bytes, want)
		}
		got = append(got, pageTestIDs(page.Records)...)
		if !page.HasMore {
			if page.Next != "" {
				t.Errorf("final page Next = %q, want empty", page.Next)
			}
			break
		}
		if page.Next == "" {
			t.Fatalf("page %d has more records but an empty Next", pages)
		}
		after = page.Next
	}

	want := pageTestIDs(records)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("walked ids = %v, want %v", got, want)
	}
	seen := make(map[string]int, len(want))
	for _, id := range got {
		seen[id]++
	}
	for _, id := range want {
		if seen[id] != 1 {
			t.Errorf("id %q seen %d times, want exactly once", id, seen[id])
		}
	}
}

// (b) fromEnd=true 返回最后一页；把 Next 传回可向前（往回）遍历，
// 直到每条记录都被恰好看到一次。
func TestReadPageFromEndReturnsLastRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tail.jsonl")
	records := pageTestRecords(10)
	writeRawSession(t, path, records)
	lineLen := pageTestLineLen(t, path)
	maxBytes := 3*lineLen + 1

	first, err := ReadPage(path, "", maxBytes, true)
	if err != nil {
		t.Fatalf("ReadPage(fromEnd): %v", err)
	}
	if want := []string{"r000008", "r000009", "r000010"}; !reflect.DeepEqual(pageTestIDs(first.Records), want) {
		t.Fatalf("last page ids = %v, want %v", pageTestIDs(first.Records), want)
	}
	if !first.HasMore || first.Next != "r000008" {
		t.Fatalf("last page HasMore=%v Next=%q, want true / %q", first.HasMore, first.Next, "r000008")
	}

	var all []string
	after := ""
	for pages := 0; ; pages++ {
		if pages >= 20 {
			t.Fatal("backward pagination did not terminate")
		}
		page, err := ReadPage(path, after, maxBytes, true)
		if err != nil {
			t.Fatalf("ReadPage(backwards page %d): %v", pages, err)
		}
		if page.Bytes > maxBytes {
			t.Fatalf("page %d bytes = %d > cap %d", pages, page.Bytes, maxBytes)
		}
		all = append(all, pageTestIDs(page.Records)...)
		if !page.HasMore {
			if page.Next != "" {
				t.Errorf("final page Next = %q, want empty", page.Next)
			}
			break
		}
		after = page.Next
	}

	seen := make(map[string]bool, len(records))
	for _, id := range all {
		if seen[id] {
			t.Errorf("id %q appeared twice in backward walk", id)
		}
		seen[id] = true
	}
	if len(seen) != len(records) {
		t.Fatalf("backward walk saw %d ids, want %d: %v", len(seen), len(records), all)
	}
	for _, record := range records {
		if !seen[record.ID] {
			t.Errorf("backward walk missed %q", record.ID)
		}
	}
}

// (c) 双向遍历时都宽松地跳过格式错误和空白行。
func TestReadPageSkipsMalformedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "malformed.jsonl")
	valid1 := `{"id":"r000001","ts":"2026-11-19T12:00:00Z","kind":"user","data":"one"}`
	valid2 := `{"id":"r000002","ts":"2026-11-19T12:00:00Z","kind":"user","data":"two"}`
	content := strings.Join([]string{valid1, "not json at all", "", "{broken", valid2}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	for _, fromEnd := range []bool{false, true} {
		page, err := ReadPage(path, "", 1<<20, fromEnd)
		if err != nil {
			t.Fatalf("ReadPage(fromEnd=%v): %v", fromEnd, err)
		}
		if want := []string{"r000001", "r000002"}; !reflect.DeepEqual(pageTestIDs(page.Records), want) {
			t.Errorf("fromEnd=%v ids = %v, want %v", fromEnd, pageTestIDs(page.Records), want)
		}
		if page.HasMore || page.Next != "" {
			t.Errorf("fromEnd=%v HasMore=%v Next=%q, want false / empty", fromEnd, page.HasMore, page.Next)
		}
	}
}

// (d) 大于整个上限的记录会整条单独返回；当后面
// 还有更多记录时，HasMore 让调用方可以继续。
func TestReadPageReturnsOversizedRecordAlone(t *testing.T) {
	big := json.RawMessage(`"` + strings.Repeat("x", 4096) + `"`)
	small := json.RawMessage(`"small"`)
	ts := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)

	t.Run("single record has no more", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "oversized.jsonl")
		writeRawSession(t, path, []Record{{ID: "big", TS: ts, Kind: KindUser, Data: big}})
		lineLen := pageTestLineLen(t, path)

		for _, fromEnd := range []bool{false, true} {
			page, err := ReadPage(path, "", 100, fromEnd)
			if err != nil {
				t.Fatalf("ReadPage(fromEnd=%v): %v", fromEnd, err)
			}
			if len(page.Records) != 1 || page.Records[0].ID != "big" {
				t.Fatalf("fromEnd=%v records = %+v, want just 'big'", fromEnd, page.Records)
			}
			if !reflect.DeepEqual([]byte(page.Records[0].Data), []byte(big)) {
				t.Errorf("fromEnd=%v oversized payload was truncated", fromEnd)
			}
			if page.Bytes != lineLen {
				t.Errorf("fromEnd=%v bytes = %d, want %d", fromEnd, page.Bytes, lineLen)
			}
			if page.HasMore || page.Next != "" {
				t.Errorf("fromEnd=%v HasMore=%v Next=%q, want false / empty", fromEnd, page.HasMore, page.Next)
			}
		}
	})

	t.Run("trailing record is reachable", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "oversized-plus.jsonl")
		writeRawSession(t, path, []Record{
			{ID: "big", TS: ts, Kind: KindUser, Data: big},
			{ID: "tail", TS: ts, Kind: KindUser, Data: small},
		})
		page, err := ReadPage(path, "", 100, false)
		if err != nil {
			t.Fatalf("ReadPage: %v", err)
		}
		if got := pageTestIDs(page.Records); !reflect.DeepEqual(got, []string{"big"}) {
			t.Fatalf("first page = %v, want [big]", got)
		}
		if !page.HasMore || page.Next != "big" {
			t.Fatalf("HasMore=%v Next=%q, want true / big", page.HasMore, page.Next)
		}
		next, err := ReadPage(path, page.Next, 100, false)
		if err != nil {
			t.Fatalf("ReadPage(next): %v", err)
		}
		if got := pageTestIDs(next.Records); !reflect.DeepEqual(got, []string{"tail"}) {
			t.Fatalf("second page = %v, want [tail]", got)
		}
		if next.HasMore {
			t.Fatalf("second page HasMore=%v, want false", next.HasMore)
		}
	})
}

// (e) 非空 afterID 对正向读取严格从该记录之后恢复。
// 越过末尾的游标返回空页。
func TestReadPageAfterIDResumes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resume.jsonl")
	records := pageTestRecords(6)
	writeRawSession(t, path, records)
	lineLen := pageTestLineLen(t, path)
	maxBytes := 2*lineLen + 1 // 每页两条记录

	first, err := ReadPage(path, "", maxBytes, false)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if want := []string{"r000001", "r000002"}; !reflect.DeepEqual(pageTestIDs(first.Records), want) {
		t.Fatalf("first page = %v, want %v", pageTestIDs(first.Records), want)
	}
	second, err := ReadPage(path, first.Next, maxBytes, false)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if want := []string{"r000003", "r000004"}; !reflect.DeepEqual(pageTestIDs(second.Records), want) {
		t.Fatalf("second page = %v, want %v", pageTestIDs(second.Records), want)
	}
	third, err := ReadPage(path, second.Next, maxBytes, false)
	if err != nil {
		t.Fatalf("third page: %v", err)
	}
	if want := []string{"r000005", "r000006"}; !reflect.DeepEqual(pageTestIDs(third.Records), want) {
		t.Fatalf("third page = %v, want %v", pageTestIDs(third.Records), want)
	}
	if third.HasMore {
		t.Fatal("third page reports more records after the last one")
	}

	// 位于最后一条记录上的游标之后没有任何内容。
	empty, err := ReadPage(path, "r000006", maxBytes, false)
	if err != nil {
		t.Fatalf("cursor at end: %v", err)
	}
	if len(empty.Records) != 0 || empty.HasMore || empty.Next != "" || empty.Bytes != 0 {
		t.Fatalf("cursor at end = %+v, want empty page", empty)
	}

	// 未知游标是宽松的：正向读取从开头开始。
	unknown, err := ReadPage(path, "does-not-exist", maxBytes, false)
	if err != nil {
		t.Fatalf("unknown cursor: %v", err)
	}
	if want := []string{"r000001", "r000002"}; !reflect.DeepEqual(pageTestIDs(unknown.Records), want) {
		t.Fatalf("unknown cursor = %v, want %v", pageTestIDs(unknown.Records), want)
	}
}

// (f) 空文件（或不存在的文件）返回空页且不报错。
func TestReadPageEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.jsonl")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	for _, fromEnd := range []bool{false, true} {
		page, err := ReadPage(path, "stale-cursor", 1024, fromEnd)
		if err != nil {
			t.Fatalf("ReadPage(empty, fromEnd=%v): %v", fromEnd, err)
		}
		if len(page.Records) != 0 || page.HasMore || page.Next != "" || page.Bytes != 0 {
			t.Errorf("empty file fromEnd=%v = %+v, want empty page", fromEnd, page)
		}
	}

	page, err := ReadPage(filepath.Join(dir, "missing.jsonl"), "", 1024, false)
	if err != nil {
		t.Fatalf("ReadPage(missing): %v", err)
	}
	if len(page.Records) != 0 || page.HasMore || page.Next != "" || page.Bytes != 0 {
		t.Errorf("missing file = %+v, want empty page", page)
	}
}

// 生成的 id 必须与 readRecords 一致，使一个读取 API 生成的游标
// 能在另一个中解析。
func TestReadPageGeneratedIDsMatchReadRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-ids.jsonl")
	writeRawSession(t, path, []Record{
		{Kind: KindUser, Data: json.RawMessage(`"a"`)},
		{Kind: KindUser, Data: json.RawMessage(`"b"`)},
	})
	full, err := readRecords(path)
	if err != nil {
		t.Fatalf("readRecords: %v", err)
	}
	page, err := ReadPage(path, "", 1<<20, false)
	if err != nil {
		t.Fatalf("ReadPage: %v", err)
	}
	// readRecords 会注入读取时的 schema 头，而 ReadPage（宽松的
	// 原始文件读取器）不会；因此只比较转录自身的记录。
	if !reflect.DeepEqual(pageTestIDs(page.Records), pageTestIDs(withoutSchemaHeader(full))) {
		t.Fatalf("ids = %v, want %v", pageTestIDs(page.Records), pageTestIDs(withoutSchemaHeader(full)))
	}
}

// Session 方法必须委托给句柄写入的同一个文件。
func TestSessionReadPageDelegates(t *testing.T) {
	s := newTestSession(t, t.TempDir(), "delegate")
	appendAll(t, s, KindUser, KindAssistant, KindTool)
	page, err := s.ReadPage("", 1<<20, false)
	if err != nil {
		t.Fatalf("Session.ReadPage: %v", err)
	}
	full, err := s.Records()
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if !reflect.DeepEqual(pageTestIDs(page.Records), pageTestIDs(withoutSchemaHeader(full))) {
		t.Fatalf("page ids = %v, want %v", pageTestIDs(page.Records), pageTestIDs(withoutSchemaHeader(full)))
	}
}
