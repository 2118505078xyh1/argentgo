package session

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// 本文件把 schema 迁移接入共享读取路径，并端到端守护该行为：
// 旧转录在读取时获得一个头，显式版本被尊重，
// 而更新的版本被拒绝而不是靠猜测
// 处理。

// withoutSchemaHeader 去掉开头的读取时 schema 头，使现有的
// 树/链测试仍能对转录自身的记录进行断言。注入的头是元数据，
// 没有 parent 且 id 为空；它永远不会加入
// id/parent 链。
func withoutSchemaHeader(records []Record) []Record {
	if len(records) > 0 && records[0].Kind == KindSchema {
		return records[1:]
	}
	return records
}

// withoutSchemaBranches 是 withoutSchemaHeader 的 Branch 对应版本。
func withoutSchemaBranches(branches []Branch) []Branch {
	out := make([]Branch, 0, len(branches))
	for _, b := range branches {
		if b.Kind != KindSchema {
			out = append(out, b)
		}
	}
	return out
}

// TestReadRecordsLegacyV0GainsHeader：通过共享读取器读取的 v0 转录
// （没有 schema 头）会升级到 v1 返回，原始记录
// 保留在注入的元数据头之后。
func TestReadRecordsLegacyV0GainsHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.jsonl")
	writeRawSession(t, path, []Record{
		{ID: "n000001", Kind: KindHeader, Data: []byte(`{"title":"legacy"}`)},
		{ID: "n000002", Parent: "n000001", Kind: KindUser, Data: []byte(`{"text":"hello"}`)},
	})

	records, err := readRecords(path)
	if err != nil {
		t.Fatalf("readRecords: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("records = %d, want 3 (schema header + 2)", len(records))
	}
	if records[0].Kind != KindSchema {
		t.Fatalf("first kind = %q, want %q", records[0].Kind, KindSchema)
	}
	// 注入的头是 id 为空的元数据：它永远不会进入
	// parent 链，也不能占用追加所使用的 id 槽位。
	if records[0].ID != "" {
		t.Fatalf("injected schema header id = %q, want empty", records[0].ID)
	}
	if records[1].ID != "n000001" || records[2].ID != "n000002" {
		t.Fatalf("original records moved: %+v", records[1:])
	}
	if records[2].Data == nil || string(records[2].Data) != `{"text":"hello"}` {
		t.Fatalf("original payload changed: %s", records[2].Data)
	}
}

// TestReadRecordsExplicitV1Unchanged：已经带有显式 v1 schema 头的转录
// 原样返回（不产生重复头）。
func TestReadRecordsExplicitV1Unchanged(t *testing.T) {
	schema, err := SchemaRecord(CurrentSchemaVersion)
	if err != nil {
		t.Fatalf("SchemaRecord: %v", err)
	}
	schema.ID = "s1"
	path := filepath.Join(t.TempDir(), "explicit.jsonl")
	writeRawSession(t, path, []Record{
		schema,
		{ID: "n000001", Kind: KindHeader, Data: []byte(`{"title":"explicit"}`)},
		{ID: "n000002", Parent: "n000001", Kind: KindUser, Data: []byte(`"hi"`)},
	})

	records, err := readRecords(path)
	if err != nil {
		t.Fatalf("readRecords: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("records = %d, want 3 (no extra header)", len(records))
	}
	if records[0].Kind != KindSchema || records[0].ID != "s1" {
		t.Fatalf("first record = %+v, want original schema header", records[0])
	}
}

// TestReadRecordsNewerVersionErrors：来自更新构建的转录是明确的错误，
// 而不是静默降级。readRecords 必须传播 ErrNewerSchemaVersion。
func TestReadRecordsNewerVersionErrors(t *testing.T) {
	schema, err := SchemaRecord(CurrentSchemaVersion + 10)
	if err != nil {
		t.Fatalf("SchemaRecord: %v", err)
	}
	schema.ID = "s1"
	path := filepath.Join(t.TempDir(), "newer.jsonl")
	writeRawSession(t, path, []Record{
		schema,
		{ID: "n000001", Kind: KindHeader, Data: []byte(`{"title":"future"}`)},
	})

	if _, err := readRecords(path); !errors.Is(err, ErrNewerSchemaVersion) {
		t.Fatalf("readRecords error = %v, want ErrNewerSchemaVersion", err)
	}
}

// TestMigratedSessionBranchesAndMessages：v0 转录在读取时迁移后，
// 对话视图不受影响——消息跟随真实的 parent
// 链接，schema 头永远不会作为消息出现。
func TestMigratedSessionBranchesAndMessages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migrate-view.jsonl")
	stamp := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	writeRawSession(t, path, []Record{
		{ID: "h", TS: stamp, Kind: KindHeader, Data: []byte(`{"title":"view"}`)},
		{ID: "u1", Parent: "h", TS: stamp, Kind: KindUser, Data: []byte(`"question"`)},
		{ID: "a1", Parent: "u1", TS: stamp, Kind: KindAssistant, Data: []byte(`"answer"`)},
	})

	branches, err := Branches(path)
	if err != nil {
		t.Fatalf("Branches: %v", err)
	}
	// 注入的 schema 头是元数据：它作为分支出现，但永远不会
	// 作为消息出现，真实记录保留其 id/parent/depth。
	sawSchema := false
	for _, b := range branches {
		if b.Kind == KindSchema {
			sawSchema = true
		}
	}
	if !sawSchema {
		t.Fatal("Branches did not include the migrated schema header")
	}
	for _, b := range branches {
		if b.ID == "u1" && (b.Parent != "h" || b.Kind != KindUser) {
			t.Fatalf("u1 branch = %+v", b)
		}
	}

	// Messages 从活动叶节点遍历并跳过元数据记录。
	session := &Session{dir: filepath.Dir(path), id: "migrate-view", active: "a1"}
	messages := session.Messages()
	if len(messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(messages))
	}
	if messages[0].Role != "user" || messages[1].Role != "assistant" {
		t.Fatalf("messages = %+v", messages)
	}
}
