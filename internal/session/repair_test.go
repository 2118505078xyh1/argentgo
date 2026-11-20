package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func validRepairLine(t *testing.T, id, parent, kind string) string {
	t.Helper()
	record := Record{
		ID:     id,
		Parent: parent,
		TS:     time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC),
		Kind:   kind,
		Data:   json.RawMessage(`{"role":"user","content":"hi"}`),
	}
	line, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return string(line)
}

func writeRepairFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertRepairFileBytes(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("file bytes = %q, want %q", data, want)
	}
}

func assertNoRepairTemps(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Errorf("staged repair file left behind: %s", entry.Name())
		}
	}
}

func TestValidateAndRepairAllValidFileUnchanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "valid.jsonl")
	first := validRepairLine(t, "n000001", "", KindHeader)
	second := validRepairLine(t, "n000002", "n000001", KindMessage)
	content := first + "\n" + second + "\n"
	writeRepairFile(t, path, content)

	for _, line := range []string{first, second} {
		if kind, detail := decodeLine(line); kind != DecodeOK {
			t.Fatalf("decodeLine(%q) = %v (%s), want DecodeOK", line, kind, detail)
		}
	}
	report, err := ValidateAndRepair(path, true)
	if err != nil {
		t.Fatalf("ValidateAndRepair: %v", err)
	}
	if report.Path != path || report.PhysicalLines != 2 || report.KeptLines != 2 ||
		report.TornTail || report.Repaired {
		t.Fatalf("report = %+v, want 2/2 lines, no torn tail, no repair", report)
	}
	assertRepairFileBytes(t, path, content)
}

func TestValidateAndRepairTornLastLineWithoutRepair(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "torn.jsonl")
	first := validRepairLine(t, "n000001", "", KindHeader)
	second := validRepairLine(t, "n000002", "n000001", KindMessage)
	content := first + "\n" + second + "\n" + `{"id":"tor`
	writeRepairFile(t, path, content)

	report, err := ValidateAndRepair(path, false)
	if err != nil {
		t.Fatalf("ValidateAndRepair: %v", err)
	}
	if !report.TornTail || report.Repaired || report.PhysicalLines != 3 || report.KeptLines != 2 {
		t.Fatalf("report = %+v, want torn tail with 2 kept lines and no repair", report)
	}
	assertRepairFileBytes(t, path, content)
	assertNoRepairTemps(t, dir)
}

func TestValidateAndRepairTornLastLineWithRepair(t *testing.T) {
	dir := t.TempDir()
	session := newTestSession(t, dir, "recover")
	appendAll(t, session, KindMessage, KindAssistant)
	before, err := session.Records()
	if err != nil {
		t.Fatal(err)
	}
	prefix, err := os.ReadFile(session.path())
	if err != nil {
		t.Fatal(err)
	}
	writeRepairFile(t, session.path(), string(prefix)+`{"kind":"entry"`)

	report, err := ValidateAndRepair(session.path(), true)
	if err != nil {
		t.Fatalf("ValidateAndRepair: %v", err)
	}
	// KeptLines 统计物理转录行数；由 Records() 添加的
	// 读取时 schema 头不在其中。
	physicalBefore := len(withoutSchemaHeader(before))
	if !report.TornTail || !report.Repaired || report.KeptLines != physicalBefore {
		t.Fatalf("report = %+v, want repaired torn tail with %d kept lines", report, physicalBefore)
	}
	assertRepairFileBytes(t, session.path(), string(prefix))

	after, err := session.Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("records = %d, want %d after repair", len(after), len(before))
	}
	for i := range before {
		if after[i].ID != before[i].ID || after[i].Kind != before[i].Kind {
			t.Errorf("record[%d] = %q/%q, want %q/%q",
				i, after[i].ID, after[i].Kind, before[i].ID, before[i].Kind)
		}
	}
	if viaRead, err := readRecords(session.path()); err != nil || len(viaRead) != len(before) {
		t.Fatalf("readRecords after repair = %d records, err %v", len(viaRead), err)
	}
	assertNoRepairTemps(t, dir)
}

// TestValidateAndRepairToleratesInteriorBlankLines 覆盖一个真实 bug：
// readRecords / ReadPage / Search 都会跳过空白行，但校验器过去把中间的
// 空白行当成语法错误，导致 OpenSession 拒绝恢复、List 直接隐藏整份会话。
// 中间的空白行必须像读取路径一样被安全忽略，且不触发任何写入。
func TestValidateAndRepairToleratesInteriorBlankLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "blank.jsonl")
	first := validRepairLine(t, "n000001", "", KindHeader)
	second := validRepairLine(t, "n000002", "n000001", KindUser)
	third := validRepairLine(t, "n000003", "n000002", KindAssistant)
	// 首行之前、两行之间、末行之后各放一个空白行（结尾的 "\n" 只是正常终止）。
	content := "\n" + first + "\n \t \n" + second + "\n\n" + third + "\n"
	writeRepairFile(t, path, content)

	report, err := ValidateAndRepair(path, true)
	if err != nil {
		t.Fatalf("ValidateAndRepair: %v", err)
	}
	if report.TornTail || report.Repaired || report.KeptLines != 3 {
		t.Fatalf("report = %+v, want 3 kept lines, no torn tail, no repair", report)
	}
	assertRepairFileBytes(t, path, content)
	assertNoRepairTemps(t, dir)

	// 读取路径看到的记录必须与没有空白行时完全一致。
	records, err := readRecords(path)
	if err != nil {
		t.Fatalf("readRecords: %v", err)
	}
	records = withoutSchemaHeader(records)
	if len(records) != 3 {
		t.Fatalf("readRecords = %d records, want 3", len(records))
	}
	for i, want := range []string{"n000001", "n000002", "n000003"} {
		if records[i].ID != want {
			t.Errorf("record[%d].ID = %q, want %q", i, records[i].ID, want)
		}
	}
}

// TestOpenSessionAndListAcceptInteriorBlankLines 从公开 API 验证同一个 bug：
// 含中间空白行的会话既不能被恢复，也不会出现在会话列表中。
func TestOpenSessionAndListAcceptInteriorBlankLines(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.NewSession("blank")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(KindUser, "hello"); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(sess.path())
	if err != nil {
		t.Fatal(err)
	}
	// 在 header 与用户消息之间插入一个空白行。
	merged := append([]byte{}, original...)
	idx := strings.Index(string(original), "\n")
	merged = append(merged[:idx+1], append([]byte("\n"), merged[idx+1:]...)...)
	writeRepairFile(t, sess.path(), string(merged))

	reopened, err := store.OpenSession(sess.ID())
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	if got := reopened.Title(); got != "blank" {
		t.Fatalf("reopened title = %q, want %q", got, "blank")
	}
	metas, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(metas) != 1 || metas[0].ID != sess.ID() {
		t.Fatalf("List metas = %+v, want only %q", metas, sess.ID())
	}
	assertRepairFileBytes(t, sess.path(), string(merged))
}

// TestValidateAndRepairTrimsTrailingBlankLinesInOnePass 确认末尾空白行被
// 当作残缺尾部裁掉，并且连带清掉其前面的空白行，一次修复即达干净状态。
func TestValidateAndRepairTrimsTrailingBlankLinesInOnePass(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trailing.jsonl")
	first := validRepairLine(t, "n000001", "", KindHeader)
	content := first + "\n\n  \n\n"
	writeRepairFile(t, path, content)

	report, err := ValidateAndRepair(path, true)
	if err != nil {
		t.Fatalf("ValidateAndRepair: %v", err)
	}
	if !report.TornTail || !report.Repaired || report.KeptLines != 1 {
		t.Fatalf("report = %+v, want torn/repaired with 1 kept line", report)
	}
	assertRepairFileBytes(t, path, first+"\n")

	again, err := ValidateAndRepair(path, false)
	if err != nil {
		t.Fatalf("ValidateAndRepair after repair: %v", err)
	}
	if again.TornTail || again.Repaired || again.PhysicalLines != 1 || again.KeptLines != 1 {
		t.Fatalf("repaired file is not clean: %+v", again)
	}
	assertNoRepairTemps(t, dir)
}

func TestValidateAndRepairTornMiddleLineIsHardCorruption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "middle.jsonl")
	first := validRepairLine(t, "n000001", "", KindHeader)
	third := validRepairLine(t, "n000003", "n000001", KindMessage)
	content := first + "\n" + `{"id":"tor` + "\n" + third + "\n"
	writeRepairFile(t, path, content)

	report, err := ValidateAndRepair(path, true)
	if !errors.Is(err, ErrHardCorruption) {
		t.Fatalf("ValidateAndRepair error = %v, want ErrHardCorruption", err)
	}
	if report.TornTail || report.Repaired {
		t.Fatalf("report = %+v, want no torn tail and no repair", report)
	}
	assertRepairFileBytes(t, path, content)
	assertNoRepairTemps(t, dir)
}

func TestValidateAndRepairSchemaErrorOnLastLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schema.jsonl")
	first := validRepairLine(t, "n000001", "", KindHeader)
	content := first + "\n" + `{"id":"x","kind":123}` + "\n"
	writeRepairFile(t, path, content)

	if kind, detail := decodeLine(`{"id":"x","kind":123}`); kind != DecodeSchema {
		t.Fatalf("decodeLine(schema) = %v (%s), want DecodeSchema", kind, detail)
	}
	report, err := ValidateAndRepair(path, true)
	if !errors.Is(err, ErrHardCorruption) {
		t.Fatalf("ValidateAndRepair error = %v, want ErrHardCorruption", err)
	}
	if report.TornTail || report.Repaired {
		t.Fatalf("report = %+v, want no torn tail and no repair", report)
	}
	assertRepairFileBytes(t, path, content)
	assertNoRepairTemps(t, dir)
}

func TestValidateAndRepairLegacyRecordWithoutID(t *testing.T) {
	line := `{"kind":"message","ts":"2026-11-19T12:00:00Z","data":{"role":"user","content":"legacy"}}`
	if kind, detail := decodeLine(line); kind != DecodeOK {
		t.Fatalf("decodeLine(legacy) = %v (%s), want DecodeOK", kind, detail)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.jsonl")
	writeRepairFile(t, path, line+"\n")

	report, err := ValidateAndRepair(path, true)
	if err != nil {
		t.Fatalf("ValidateAndRepair: %v", err)
	}
	if report.TornTail || report.Repaired || report.KeptLines != 1 {
		t.Fatalf("report = %+v, want one valid line and no repair", report)
	}
	assertRepairFileBytes(t, path, line+"\n")
	records, err := readRecords(path)
	records = withoutSchemaHeader(records)
	if err != nil || len(records) != 1 || records[0].ID != "n000001" {
		t.Fatalf("readRecords = %+v, err %v; want one synthesized id", records, err)
	}
}

func TestValidateAndRepairAppendsMissingTrailingNewline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "unterminated.jsonl")
	first := validRepairLine(t, "n000001", "", KindHeader)
	second := validRepairLine(t, "n000002", "n000001", KindMessage)
	content := first + "\n" + second
	writeRepairFile(t, path, content)

	report, err := ValidateAndRepair(path, true)
	if err != nil {
		t.Fatalf("ValidateAndRepair: %v", err)
	}
	if report.TornTail || !report.Repaired || report.KeptLines != 2 {
		t.Fatalf("report = %+v, want a newline repair of two valid lines", report)
	}
	assertRepairFileBytes(t, path, content+"\n")

	// 列表式校验从不写入，即使缺少换行符也是如此。
	writeRepairFile(t, path, content)
	report, err = ValidateAndRepair(path, false)
	if err != nil {
		t.Fatalf("ValidateAndRepair(repair=false): %v", err)
	}
	if report.Repaired || report.TornTail {
		t.Fatalf("report = %+v, want no write when repair is false", report)
	}
	assertRepairFileBytes(t, path, content)
}

// TestValidateAndRepairIsIdempotentWithoutValidPrefix 覆盖首行残缺
// （或空/空白文件）的情形：修复不得发布一个裸 "\n"，因为
// decodeLine 会把它归类为又一处残缺尾部。修复后的转录必须
// 在下次校验时是干净的，否则每次恢复都会再次重写文件。
func TestValidateAndRepairIsIdempotentWithoutValidPrefix(t *testing.T) {
	for _, tc := range []struct {
		name     string
		content  string
		wantTorn bool
	}{
		{"empty file", "", false},
		{"trailing newline only", "\n", true},
		{"whitespace only", "   \n", true},
		{"torn first line", `{"id":"tor`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "torn.jsonl")
			writeRepairFile(t, path, tc.content)

			report, err := ValidateAndRepair(path, true)
			if err != nil {
				t.Fatalf("ValidateAndRepair(repair=true): %v", err)
			}
			if report.TornTail != tc.wantTorn || report.Repaired != tc.wantTorn || report.KeptLines != 0 {
				t.Fatalf("report = %+v, want torn/repaired=%v with zero kept lines", report, tc.wantTorn)
			}
			assertRepairFileBytes(t, path, "")

			again, err := ValidateAndRepair(path, false)
			if err != nil {
				t.Fatalf("ValidateAndRepair after repair: %v", err)
			}
			if again.TornTail || again.Repaired || again.PhysicalLines != 0 {
				t.Fatalf("repaired file is reported torn again: %+v", again)
			}
		})
	}
}

// TestValidateAndRepairKeepsValidPrefixBeforeTornTail 确保对首行残缺的修复
// 在确有有效前缀存活时不会剥离结束换行符：
// 保留的记录必须保持逐字节一致并干净地结束。
func TestValidateAndRepairKeepsValidPrefixBeforeTornTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tail.jsonl")
	first := validRepairLine(t, "n000001", "", KindHeader)
	content := first + "\n" + `{"id":"tor`
	writeRepairFile(t, path, content)

	report, err := ValidateAndRepair(path, true)
	if err != nil {
		t.Fatalf("ValidateAndRepair: %v", err)
	}
	if !report.TornTail || !report.Repaired || report.KeptLines != 1 {
		t.Fatalf("report = %+v, want one kept line repaired", report)
	}
	assertRepairFileBytes(t, path, first+"\n")

	again, err := ValidateAndRepair(path, false)
	if err != nil {
		t.Fatalf("ValidateAndRepair after repair: %v", err)
	}
	if again.TornTail || again.PhysicalLines != 1 || again.KeptLines != 1 {
		t.Fatalf("repaired prefix is not clean: %+v", again)
	}
}

func TestAtomicPublishJSONLWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows rename and directory-sync path")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "publish.jsonl")
	writeRepairFile(t, path, "stale\n")
	data := []byte(validRepairLine(t, "n000001", "", KindHeader) + "\n")

	// 目录 Sync 在 Windows 上返回 EINVAL；它不能让发布失败。
	if err := atomicPublishJSONL(path, data); err != nil {
		t.Fatalf("atomicPublishJSONL: %v (directory Sync EINVAL must be ignored)", err)
	}
	assertRepairFileBytes(t, path, string(data))
	assertNoRepairTemps(t, dir)
}

func TestOpenSessionRejectsHardCorruptTranscript(t *testing.T) {
	dir := t.TempDir()
	session := newTestSession(t, dir, "corrupt")
	content := validRepairLine(t, "n000001", "", KindHeader) + "\n" +
		`{"id":"x","kind":123}` + "\n"
	writeRepairFile(t, session.path(), content)

	_, err := OpenSession(dir, session.ID())
	if err == nil {
		t.Fatal("OpenSession accepted a hard-corrupt transcript")
	}
	if !errors.Is(err, ErrHardCorruption) || !strings.Contains(err.Error(), "resume session") {
		t.Fatalf("OpenSession error = %v, want wrapped ErrHardCorruption", err)
	}
	assertRepairFileBytes(t, session.path(), content)
	assertNoRepairTemps(t, dir)
}

func TestListSkipsHardCorruptTranscript(t *testing.T) {
	dir := t.TempDir()
	good := newTestSession(t, dir, "good")
	bad := newTestSession(t, dir, "bad")
	content := `{"id":"x","kind":123}` + "\n"
	writeRepairFile(t, bad.path(), content)

	metas, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(metas) != 1 || metas[0].ID != good.ID() {
		t.Fatalf("List metas = %+v, want only %s", metas, good.ID())
	}
	found, err := Find(dir, "")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(found) != 1 || found[0].ID != good.ID() {
		t.Fatalf("Find metas = %+v, want only %s", found, good.ID())
	}
	assertRepairFileBytes(t, bad.path(), content)
	assertNoRepairTemps(t, dir)
}
