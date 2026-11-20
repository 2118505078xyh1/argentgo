package session

import (
	"encoding/json"
	"errors"
	"fmt"
)

// CurrentSchemaVersion 是本版本写入和读取的 JSONL schema 版本。
// 仅当现有记录的*含义*发生变化时才会提升它：添加新的未知 kind 无需提升，
// 因为 kind 是开放的（见 Record）。
const CurrentSchemaVersion = 1

// KindSchema 标记显式的 schema 头记录。与 KindHeader 一样，它是元数据：
// 对话视图会忽略它，因此插入一条永远不会改变会话产生的消息。
const KindSchema = "schema"

// ErrNewerSchemaVersion 表示转录由比本版本更新的构建写入。
// MigrateRecords 拒绝猜测如何降级这样的文件。
var ErrNewerSchemaVersion = errors.New("session: schema version is newer than supported")

// SchemaPayload 是 KindSchema 头记录携带的数据。
type SchemaPayload struct {
	Version int `json:"version"`
}

// SchemaRecord 为 version 构建显式的 schema 头记录。它是纯构造函数：
// 返回的 Record 只携带 Kind 和 Data，ID/Parent/TS 留给调用方（或追加路径）填充。
func SchemaRecord(version int) (Record, error) {
	if version < 0 {
		return Record{}, fmt.Errorf("session: negative schema version %d", version)
	}
	data, err := encodePayload(SchemaPayload{Version: version})
	if err != nil {
		return Record{}, err
	}
	return Record{Kind: KindSchema, Data: data}, nil
}

// migration 将记录从一个 schema 版本升级到下一个版本。每一步必须是纯函数：
// 返回新切片，永不修改输入。当 CurrentSchemaVersion 提升时，为源版本 N 注册一步，
// 将 N 升级到 N+1。
type migration func(records []Record) ([]Record, error)

// migrations 是按源版本索引的升级钩子表。目前唯一的一步是 v0 -> v1
// （给旧的无头转录加上显式头）。将来 v1 -> v2 的提升会在这里添加
// `1: migrateV1ToV2` 条目，而无需改动 MigrateRecords。
var migrations = map[int]migration{
	0: migrateV0ToV1,
}

// migrateV0ToV1 通过给转录添加显式 schema 头，将旧格式升级到 v1。
// 如果已存在 v0 头，则原地更新其 version 字段；否则在前面加一个新头。
// 其他每条记录都逐字保留其顺序、id、parent 和载荷。
func migrateV0ToV1(records []Record) ([]Record, error) {
	header, err := SchemaRecord(1)
	if err != nil {
		return nil, err
	}
	out := make([]Record, len(records))
	copy(out, records)
	found := false
	for i := range out {
		if out[i].Kind != KindSchema {
			continue
		}
		// 必须升级每一个 schema 头，不能只改第一个：schemaVersion 允许多个
		// 版本一致的头记录（例如合并两段转录），若只升级其中一个，结果里会
		// 残留版本 0 的头，再次调用 MigrateRecords 就会报头冲突，破坏幂等性；
		// 一旦这份结果被写回磁盘，后续读取会直接失败。
		// 逐条复制字节，避免多个头记录共享同一底层切片。
		out[i].Data = append(json.RawMessage(nil), header.Data...)
		found = true
	}
	if !found {
		out = append([]Record{header}, out...)
	}
	return out, nil
}

// MigrateRecords 将记录升级到 CurrentSchemaVersion。它是纯函数（输入永不被修改）且幂等：
// 已处于当前版本的转录原样返回，因此重运行是空操作。没有 schema 头的转录按 v0 处理；
// 携带更新版本的转录会被拒绝并返回 ErrNewerSchemaVersion，而不是被静默降级。
// 未知 kind 始终逐字保留。
func MigrateRecords(records []Record) ([]Record, error) {
	if len(records) == 0 {
		return records, nil
	}
	version, _, err := schemaVersion(records)
	if err != nil {
		return nil, err
	}
	if version > CurrentSchemaVersion {
		return nil, fmt.Errorf("%w: record has version %d, current is %d",
			ErrNewerSchemaVersion, version, CurrentSchemaVersion)
	}

	out := append([]Record(nil), records...)
	for v := version; v < CurrentSchemaVersion; v++ {
		step, ok := migrations[v]
		if !ok {
			return nil, fmt.Errorf("session: no migration from schema version %d to %d", v, v+1)
		}
		out, err = step(out)
		if err != nil {
			return nil, fmt.Errorf("session: migrate schema %d -> %d: %w", v, v+1, err)
		}
	}
	return out, nil
}

// schemaVersion 报告 schema 头记录声明的版本；当转录没有头时返回 0。
// 后一种情况下 headerIndex 为 -1。格式错误或冲突的头会被报错，
// 绝不会被静默当作 v0。
func schemaVersion(records []Record) (version, headerIndex int, err error) {
	headerIndex = -1
	for i := range records {
		if records[i].Kind != KindSchema {
			continue
		}
		var payload SchemaPayload
		if err := json.Unmarshal(records[i].Data, &payload); err != nil {
			return 0, i, fmt.Errorf("session: decode schema header at record %d: %w", i, err)
		}
		if payload.Version < 0 {
			return 0, i, fmt.Errorf("session: negative schema version %d at record %d", payload.Version, i)
		}
		if headerIndex >= 0 && payload.Version != version {
			return 0, i, fmt.Errorf("session: conflicting schema headers: version %d and %d",
				version, payload.Version)
		}
		version, headerIndex = payload.Version, i
	}
	if headerIndex < 0 {
		return 0, -1, nil
	}
	return version, headerIndex, nil
}
