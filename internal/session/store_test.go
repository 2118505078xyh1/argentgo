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

// listByFullScan 是索引出现前的实现，作为语义基线保留：
// 解析每一份转录，不信任任何旁车文件。
func listByFullScan(t *testing.T, dir string) []Meta {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	metas := make([]Meta, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), fileExt) {
			continue
		}
		id := entry.Name()[:len(entry.Name())-len(fileExt)]
		records, err := readRecords(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		meta := metaFromRecords(id, records)
		if meta.Updated.IsZero() {
			if info, err := entry.Info(); err == nil {
				meta.Updated = info.ModTime()
			}
		}
		metas = append(metas, meta)
	}
	sort.Slice(metas, func(i, j int) bool {
		if metas[i].Updated.Equal(metas[j].Updated) {
			return metas[i].ID > metas[j].ID
		}
		return metas[i].Updated.After(metas[j].Updated)
	})
	return metas
}

func assertMetasEqual(t *testing.T, got, want []Meta) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("metas = %d, want %d: %+v", len(got), len(want), got)
	}
	for i := range got {
		if got[i].ID != want[i].ID || got[i].Title != want[i].Title ||
			got[i].Turns != want[i].Turns || !got[i].Updated.Equal(want[i].Updated) {
			t.Errorf("meta[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// pinSidecars 把所有 .idx 的时间往前移，使重建可观察：
// 缓存从不检查 stat 的新鲜度，只检查转录本身。
func pinSidecars(t *testing.T, dir string) time.Time {
	t.Helper()
	past := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), idxExt) {
			continue
		}
		if err := os.Chtimes(filepath.Join(dir, entry.Name()), past, past); err != nil {
			t.Fatal(err)
		}
	}
	return past
}

func assertSidecarsUntouched(t *testing.T, dir string, past time.Time) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), idxExt) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		if !info.ModTime().Equal(past) {
			t.Errorf("sidecar %s was rewritten (mtime %v, want %v)", entry.Name(), info.ModTime(), past)
		}
	}
}

func TestListUsesSidecarWhenFresh(t *testing.T) {
	dir := t.TempDir()
	alpha := newTestSession(t, dir, "alpha")
	appendAll(t, alpha, KindUser, KindAssistant, KindTool)
	beta := newTestSession(t, dir, "beta")
	if err := beta.Append(KindMessage, map[string]string{"role": "assistant", "content": "hi"}); err != nil {
		t.Fatal(err)
	}
	writeRawSession(t, filepath.Join(dir, "raw.jsonl"), []Record{
		{ID: "n1", TS: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), Kind: KindHeader, Data: json.RawMessage(`{"title":"raw"}`)},
		{ID: "n2", Parent: "n1", TS: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), Kind: KindAssistant, Data: json.RawMessage(`"done"`)},
	})

	want := listByFullScan(t, dir)
	got, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	assertMetasEqual(t, got, want)

	// 旁车文件保存的正是全量扫描派生出的 meta。
	info, err := os.Stat(alpha.path())
	if err != nil {
		t.Fatal(err)
	}
	cached, ok := loadIndex(alpha.path(), info, alpha.ID())
	if !ok || cached.Title != "alpha" || cached.Turns != 1 {
		t.Fatalf("cached meta = %+v (ok=%v), want alpha/1 turn", cached, ok)
	}

	// 第二次 List 必须读取旁车文件，而不是重新解析转录。
	past := pinSidecars(t, dir)
	got, err = List(dir)
	if err != nil {
		t.Fatalf("List (cached): %v", err)
	}
	assertMetasEqual(t, got, want)
	assertSidecarsUntouched(t, dir, past)
}

func TestListFallsBackOnCorruptOrStaleSidecar(t *testing.T) {
	t.Run("garbage", func(t *testing.T) {
		dir := t.TempDir()
		s := newTestSession(t, dir, "task")
		appendAll(t, s, KindUser, KindAssistant)
		if _, err := List(dir); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(s.path()+idxExt, []byte("not json"), 0o644); err != nil {
			t.Fatal(err)
		}
		want := listByFullScan(t, dir)
		got, err := List(dir)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		assertMetasEqual(t, got, want)
		info, err := os.Stat(s.path())
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := loadIndex(s.path(), info, s.ID()); !ok {
			t.Fatal("corrupt sidecar was not rebuilt")
		}
	})

	t.Run("wrong version", func(t *testing.T) {
		dir := t.TempDir()
		s := newTestSession(t, dir, "task")
		if _, err := List(dir); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(s.path() + idxExt)
		if err != nil {
			t.Fatal(err)
		}
		var entry indexEntry
		if err := json.Unmarshal(data, &entry); err != nil {
			t.Fatal(err)
		}
		entry.Version++
		data, err = json.Marshal(entry)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(s.path()+idxExt, data, 0o644); err != nil {
			t.Fatal(err)
		}
		want := listByFullScan(t, dir)
		got, err := List(dir)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		assertMetasEqual(t, got, want)
	})

	t.Run("stale after external append", func(t *testing.T) {
		dir := t.TempDir()
		s := newTestSession(t, dir, "task")
		if _, err := List(dir); err != nil {
			t.Fatal(err)
		}
		record := Record{
			ID: "n000002", Parent: "n000001",
			TS:   time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC),
			Kind: KindAssistant, Data: json.RawMessage(`"later reply"`),
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

		want := listByFullScan(t, dir)
		got, err := List(dir)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		assertMetasEqual(t, got, want)
		if got[0].Turns != 1 || !got[0].Updated.Equal(record.TS) {
			t.Fatalf("stale sidecar was trusted: %+v", got[0])
		}
	})
}

func TestListRefreshesSidecarAfterAppend(t *testing.T) {
	dir := t.TempDir()
	s := newTestSession(t, dir, "task")
	if _, err := List(dir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(s.path())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := loadIndex(s.path(), info, s.ID()); !ok {
		t.Fatal("initial sidecar missing")
	}

	if err := s.Append(KindAssistant, "reply"); err != nil {
		t.Fatalf("Append: %v", err)
	}
	// 追加通过 Session.Append 进行，若不修改 session.go 就无法从 store 中
	// 挂接它。stat 检查可检测增长。
	newInfo, err := os.Stat(s.path())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := loadIndex(s.path(), newInfo, s.ID()); ok {
		t.Fatal("sidecar still matches a transcript that grew")
	}

	want := listByFullScan(t, dir)
	got, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	assertMetasEqual(t, got, want)
	refreshed, err := os.Stat(s.path())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := loadIndex(s.path(), refreshed, s.ID()); !ok {
		t.Fatal("sidecar was not refreshed after append")
	}
	if got[0].Turns != 1 {
		t.Fatalf("turns = %d, want 1: %+v", got[0].Turns, got[0])
	}
}

func TestDeleteRemovesSidecar(t *testing.T) {
	dir := t.TempDir()
	s := newTestSession(t, dir, "gone")
	if _, err := List(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.path() + idxExt); err != nil {
		t.Fatalf("sidecar not created: %v", err)
	}
	if err := Delete(dir, s.ID()); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(s.path() + idxExt); !os.IsNotExist(err) {
		t.Fatalf("sidecar survived Delete: %v", err)
	}
}

// TestListTrustsFreshSidecarWithoutFullValidation 固化了让 List 每份转录保持 O(1) 的契约：
// 只有在精确字节解析干净之后，才会写入大小与 mtime 匹配的旁车文件，
// 因此 List 不会重新读取或重新校验转录。信任边界是 (size, mtime)；
// 保持两者不变的重写会一直不可见，
// 直到有东西再次触碰该文件。
func TestListTrustsFreshSidecarWithoutFullValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trust.jsonl")
	writeRawSession(t, path, []Record{
		{ID: "n1", TS: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), Kind: KindHeader, Data: json.RawMessage(`{"title":"trust"}`)},
		{ID: "n2", Parent: "n1", TS: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), Kind: KindAssistant, Data: json.RawMessage(`"done"`)},
	})
	if _, err := List(dir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	mtime := info.ModTime()

	// 把最后一行重写为严重损坏（数字 kind），同时保持字节长度和记录的 mtime，
	// 使旁车文件仍然匹配。
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	nl := strings.LastIndexByte(string(content), '\n')
	if nl < 0 {
		t.Fatal("fixture must be newline-terminated")
	}
	evil := []byte(`{"id":"x","kind":123}`)
	if len(evil) < len(content)-nl-1 {
		pad := make([]byte, len(content)-nl-1-len(evil))
		for i := range pad {
			pad[i] = ' '
		}
		evil = append(evil, pad...)
	}
	evil = evil[:len(content)-nl-1]
	if err := os.WriteFile(path, append(content[:nl+1], evil...), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}

	metas, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(metas) != 1 || metas[0].ID != "trust" {
		t.Fatalf("List metas = %+v, want the sidecar-trusted session", metas)
	}
}
