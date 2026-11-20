package session

import (
	"encoding/json"
	"errors"
	"fmt"
)

// CurrentSchemaVersion is the JSONL schema version this build writes and reads.
// It is bumped only when the *meaning* of an existing record changes: adding a
// new, unknown kind needs no bump because kinds are open (see Record).
const CurrentSchemaVersion = 1

// KindSchema marks the explicit schema header record. Like KindHeader it is
// metadata: the conversation view ignores it, so inserting one never changes
// which messages a session yields.
const KindSchema = "schema"

// ErrNewerSchemaVersion reports a transcript written by a newer build than this
// one. MigrateRecords refuses to guess how to downgrade such a file.
var ErrNewerSchemaVersion = errors.New("session: schema version is newer than supported")

// SchemaPayload is the data carried by a KindSchema header record.
type SchemaPayload struct {
	Version int `json:"version"`
}

// SchemaRecord builds the explicit schema header record for version. It is a
// pure constructor: the returned Record carries only Kind and Data, leaving
// ID/Parent/TS for the caller (or the append path) to fill in.
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

// migration upgrades records from one schema version to the next. A step must
// be pure: it returns a new slice and never mutates its input. Register a step
// for source version N to upgrade N -> N+1 when CurrentSchemaVersion is bumped.
type migration func(records []Record) ([]Record, error)

// migrations is the upgrade hook table, keyed by source version. Today the only
// step is v0 -> v1 (give the legacy, header-less transcript an explicit header).
// A future v1 -> v2 bump adds a `1: migrateV1ToV2` entry here without touching
// MigrateRecords.
var migrations = map[int]migration{
	0: migrateV0ToV1,
}

// migrateV0ToV1 upgrades the legacy format to v1 by giving the transcript an
// explicit schema header. If a v0 header is already present its version field
// is updated in place; otherwise a new header is prepended. Every other record
// keeps its order, ids, parents and payload verbatim.
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

// MigrateRecords upgrades records to CurrentSchemaVersion. It is pure (the
// input is never mutated) and idempotent: a transcript already at the current
// version is returned unchanged, so re-running it is a no-op. A transcript with
// no schema header is treated as v0; one carrying a newer version is rejected
// with ErrNewerSchemaVersion rather than being silently downgraded. Unknown
// kinds are always preserved verbatim.
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

// schemaVersion reports the version declared by the schema header record, or 0
// when the transcript has none. headerIndex is -1 in the latter case. A
// malformed or conflicting header is an error so it is never silently treated
// as v0.
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
