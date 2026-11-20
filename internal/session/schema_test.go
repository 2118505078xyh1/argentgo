package session

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func testRecord(id, kind, data string) Record {
	return Record{ID: id, Kind: kind, Data: json.RawMessage(data)}
}

func schemaHeader(t *testing.T, version int) Record {
	t.Helper()
	record, err := SchemaRecord(version)
	if err != nil {
		t.Fatalf("SchemaRecord(%d): %v", version, err)
	}
	return record
}

func TestSchemaRecordBuildsHeader(t *testing.T) {
	record := schemaHeader(t, CurrentSchemaVersion)
	if record.Kind != KindSchema {
		t.Fatalf("kind = %q, want %q", record.Kind, KindSchema)
	}
	var payload SchemaPayload
	if err := json.Unmarshal(record.Data, &payload); err != nil {
		t.Fatalf("unmarshal schema data: %v", err)
	}
	if payload.Version != CurrentSchemaVersion {
		t.Fatalf("version = %d, want %d", payload.Version, CurrentSchemaVersion)
	}
	if got := string(record.Data); got != `{"version":1}` {
		t.Fatalf("data = %s, want %s", got, `{"version":1}`)
	}
}

func TestSchemaRecordRejectsNegativeVersion(t *testing.T) {
	if _, err := SchemaRecord(-1); err == nil {
		t.Fatal("SchemaRecord(-1) succeeded, want error")
	}
}

// TestMigrateRecordsLegacyV0AddsHeader：没有 schema 头的转录是 v0，
// 升级时只在顶部增加一个头；其他内容不变。
func TestMigrateRecordsLegacyV0AddsHeader(t *testing.T) {
	records := []Record{
		testRecord("a", KindHeader, `{"title":"t"}`),
		testRecord("b", KindUser, `{"text":"hello"}`),
	}
	got, err := MigrateRecords(records)
	if err != nil {
		t.Fatalf("MigrateRecords: %v", err)
	}
	if len(got) != len(records)+1 {
		t.Fatalf("len = %d, want %d", len(got), len(records)+1)
	}
	if got[0].Kind != KindSchema {
		t.Fatalf("first kind = %q, want %q", got[0].Kind, KindSchema)
	}
	var payload SchemaPayload
	if err := json.Unmarshal(got[0].Data, &payload); err != nil {
		t.Fatalf("unmarshal header: %v", err)
	}
	if payload.Version != CurrentSchemaVersion {
		t.Fatalf("version = %d, want %d", payload.Version, CurrentSchemaVersion)
	}
	if !reflect.DeepEqual(got[1:], records) {
		t.Fatalf("original records changed: got %#v", got[1:])
	}
}

// TestMigrateRecordsExplicitV1Unchanged：已处于当前版本的转录
// 逐字节原样通过。
func TestMigrateRecordsExplicitV1Unchanged(t *testing.T) {
	records := []Record{
		schemaHeader(t, 1),
		testRecord("b", KindUser, `{"text":"hi"}`),
	}
	got, err := MigrateRecords(records)
	if err != nil {
		t.Fatalf("MigrateRecords: %v", err)
	}
	if !reflect.DeepEqual(got, records) {
		t.Fatalf("v1 transcript was modified:\ngot  %#v\nwant %#v", got, records)
	}
}

// TestMigrateRecordsIdempotent：迁移两次与迁移一次结果相同，
// 无论输入是旧版（v0）还是已处于当前版本（v1）。
func TestMigrateRecordsIdempotent(t *testing.T) {
	inputs := [][]Record{
		{testRecord("a", KindUser, `{"text":"x"}`)},
		{schemaHeader(t, 1), testRecord("a", KindUser, `{"text":"x"}`)},
	}
	for i, records := range inputs {
		once, err := MigrateRecords(records)
		if err != nil {
			t.Fatalf("input %d: first pass: %v", i, err)
		}
		twice, err := MigrateRecords(once)
		if err != nil {
			t.Fatalf("input %d: second pass: %v", i, err)
		}
		if !reflect.DeepEqual(twice, once) {
			t.Fatalf("input %d: not idempotent:\nonce  %#v\ntwice %#v", i, once, twice)
		}
	}
}

// TestMigrateRecordsV0HeaderUpdatedInPlace：显式的 v0 头被原地提升，
// 而不是被复制。
func TestMigrateRecordsV0HeaderUpdatedInPlace(t *testing.T) {
	records := []Record{
		schemaHeader(t, 0),
		testRecord("b", KindUser, `{"text":"x"}`),
	}
	got, err := MigrateRecords(records)
	if err != nil {
		t.Fatalf("MigrateRecords: %v", err)
	}
	if len(got) != len(records) {
		t.Fatalf("len = %d, want %d (no duplicate header)", len(got), len(records))
	}
	var payload SchemaPayload
	if err := json.Unmarshal(got[0].Data, &payload); err != nil {
		t.Fatalf("unmarshal header: %v", err)
	}
	if payload.Version != CurrentSchemaVersion {
		t.Fatalf("version = %d, want %d", payload.Version, CurrentSchemaVersion)
	}
	if got[1].ID != "b" {
		t.Fatalf("second record id = %q, want b", got[1].ID)
	}
}

// TestMigrateRecordsMultipleV0HeadersIdempotent 覆盖多头记录的升级：当转录里
// 出现多个版本一致的 v0 schema 头时，迁移必须把它们全部升级，并保证再次迁移
// 结果与首次完全一致（幂等）。此前只升级第一个头，残留的 v0 头会在第二次
// 迁移时触发“头冲突”错误。
func TestMigrateRecordsMultipleV0HeadersIdempotent(t *testing.T) {
	records := []Record{
		testRecord("h1", KindSchema, `{"version":0}`),
		testRecord("h2", KindSchema, `{"version":0}`),
		testRecord("h3", KindSchema, `{"version":0}`),
		testRecord("u", KindUser, `{"text":"hello"}`),
	}
	once, err := MigrateRecords(records)
	if err != nil {
		t.Fatalf("首次 MigrateRecords: %v", err)
	}
	// 头记录数量与顺序不变，且每个头都升级到当前版本。
	if len(once) != len(records) {
		t.Fatalf("记录数 = %d，期望 %d", len(once), len(records))
	}
	for i := 0; i < 3; i++ {
		if once[i].Kind != KindSchema {
			t.Fatalf("记录 %d 的 kind = %q，期望 %q", i, once[i].Kind, KindSchema)
		}
		var payload SchemaPayload
		if err := json.Unmarshal(once[i].Data, &payload); err != nil {
			t.Fatalf("解析头 %d: %v", i, err)
		}
		if payload.Version != CurrentSchemaVersion {
			t.Errorf("头 %d 的版本 = %d，期望 %d", i, payload.Version, CurrentSchemaVersion)
		}
	}
	if !reflect.DeepEqual(once[3], records[3]) {
		t.Fatalf("非头记录被修改: got %#v, want %#v", once[3], records[3])
	}

	// 再次迁移必须无错误且结果不变。
	twice, err := MigrateRecords(once)
	if err != nil {
		t.Fatalf("第二次 MigrateRecords（幂等性）: %v", err)
	}
	if !reflect.DeepEqual(twice, once) {
		t.Fatalf("迁移不幂等:\n首次 %#v\n再次 %#v", once, twice)
	}

	// 各头的 Data 不共享底层数组，避免修改其中一个影响其他头记录。
	if len(once) > 1 && &once[0].Data[0] == &once[1].Data[0] {
		t.Fatal("多个 schema 头共享了 Data 底层数组")
	}
}

// TestMigrateRecordsNewerVersionRejected：来自更新版本的头必须产生
// 明确的错误，绝不能静默降级。
func TestMigrateRecordsNewerVersionRejected(t *testing.T) {
	header := schemaHeader(t, 999)
	records := []Record{header, testRecord("b", KindUser, `{"text":"x"}`)}
	_, err := MigrateRecords(records)
	if err == nil {
		t.Fatal("MigrateRecords succeeded on a newer version, want error")
	}
	if !errors.Is(err, ErrNewerSchemaVersion) {
		t.Fatalf("err = %v, want ErrNewerSchemaVersion", err)
	}
	if !strings.Contains(err.Error(), "999") {
		t.Fatalf("err = %q, want it to mention the offending version", err)
	}
}

// TestMigrateRecordsEmpty：空转录是空操作。
func TestMigrateRecordsEmpty(t *testing.T) {
	got, err := MigrateRecords(nil)
	if err != nil {
		t.Fatalf("MigrateRecords(nil): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("len = %d, want 0", len(got))
	}
	got, err = MigrateRecords([]Record{})
	if err != nil {
		t.Fatalf("MigrateRecords(empty): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("len = %d, want 0", len(got))
	}
}

// TestMigrateRecordsPreservesUnknownKinds：kind 是开放的；无法识别的 kind
// 在迁移中保持原样。
func TestMigrateRecordsPreservesUnknownKinds(t *testing.T) {
	unknown := testRecord("u", "future_kind_from_newer_build", `{"opaque":[1,2,3]}`)
	records := []Record{
		testRecord("a", KindUser, `{"text":"x"}`),
		unknown,
	}
	got, err := MigrateRecords(records)
	if err != nil {
		t.Fatalf("MigrateRecords: %v", err)
	}
	found := false
	for _, record := range got {
		if record.ID == "u" {
			found = true
			if !reflect.DeepEqual(record, unknown) {
				t.Fatalf("unknown kind changed: got %#v, want %#v", record, unknown)
			}
		}
	}
	if !found {
		t.Fatal("unknown kind record was dropped")
	}
}

// TestMigrateRecordsCJK：非 ASCII 载荷原样通过。
func TestMigrateRecordsCJK(t *testing.T) {
	cjk := testRecord("b", KindUser, `{"text":"你好，世界 — 日本語・한국어 🚀"}`)
	records := []Record{
		testRecord("a", KindHeader, `{"title":"会话标题"}`),
		cjk,
	}
	got, err := MigrateRecords(records)
	if err != nil {
		t.Fatalf("MigrateRecords: %v", err)
	}
	if len(got) != len(records)+1 {
		t.Fatalf("len = %d, want %d", len(got), len(records)+1)
	}
	if !reflect.DeepEqual(got[1:], records) {
		t.Fatalf("CJK records changed:\ngot  %#v\nwant %#v", got[1:], records)
	}
	if !strings.Contains(string(got[2].Data), "你好，世界") {
		t.Fatalf("CJK text lost: %s", got[2].Data)
	}
}

// TestMigrateRecordsDoesNotMutateInput：调用方的切片永不被修改。
func TestMigrateRecordsDoesNotMutateInput(t *testing.T) {
	original := []Record{
		testRecord("a", KindUser, `{"text":"x"}`),
		testRecord("b", KindAssistant, `{"text":"y"}`),
	}
	snapshot := append([]Record(nil), original...)
	if _, err := MigrateRecords(original); err != nil {
		t.Fatalf("MigrateRecords: %v", err)
	}
	if !reflect.DeepEqual(original, snapshot) {
		t.Fatalf("input mutated:\ngot  %#v\nwant %#v", original, snapshot)
	}
}

// TestMigrateRecordsMalformedHeader：损坏的头会被报告，而不是
// 被静默当作 v0。
func TestMigrateRecordsMalformedHeader(t *testing.T) {
	records := []Record{testRecord("h", KindSchema, `not json`)}
	if _, err := MigrateRecords(records); err == nil {
		t.Fatal("MigrateRecords succeeded on a malformed header, want error")
	}
}

// TestMigrateRecordsConflictingHeaders：两个版本不一致的头会被报告，
// 而不是靠猜测处理。
func TestMigrateRecordsConflictingHeaders(t *testing.T) {
	records := []Record{
		schemaHeader(t, 1),
		schemaHeader(t, 0),
	}
	if _, err := MigrateRecords(records); err == nil {
		t.Fatal("MigrateRecords succeeded on conflicting headers, want error")
	}
}
