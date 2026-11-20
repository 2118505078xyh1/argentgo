package session

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
)

// TestSyntheticIDAvoidsExplicitCollision 覆盖 session 审计 #5：没有 id 的记录
// 绝不能被赋予与同一文件中显式 n000001 相同的 id，
// 无论哪个先出现。三条宽松读取路径必须一致，
// 否则一个路径生成的页游标无法被另一个
// 解析。
func TestSyntheticIDAvoidsExplicitCollision(t *testing.T) {
	for _, tc := range []struct {
		name    string
		records []Record
		wantIDs []string
	}{
		{
			name: "missing id before explicit n000001",
			records: []Record{
				{Kind: KindHeader, Data: json.RawMessage(`{"title":"old header"}`)},
				{ID: "n000001", Kind: KindHeader, Data: json.RawMessage(`{"title":"explicit header"}`)},
			},
			wantIDs: []string{"n000002", "n000001"},
		},
		{
			name: "explicit n000001 before missing id",
			records: []Record{
				{ID: "n000001", Kind: KindHeader, Data: json.RawMessage(`{"title":"explicit header"}`)},
				{Kind: KindUser, Data: json.RawMessage(`"anonymous"`)},
			},
			wantIDs: []string{"n000001", "n000002"},
		},
		{
			name: "explicit n000001 and n000002 leave n000003 free",
			records: []Record{
				{Kind: KindUser, Data: json.RawMessage(`"first anonymous"`)},
				{ID: "n000001", Kind: KindHeader, Data: json.RawMessage(`{"title":"one"}`)},
				{Kind: KindUser, Data: json.RawMessage(`"second anonymous"`)},
				{ID: "n000002", Kind: KindUser, Data: json.RawMessage(`"two"`)},
			},
			wantIDs: []string{"n000003", "n000001", "n000004", "n000002"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "collide.jsonl")
			writeRawSession(t, path, tc.records)

			full, err := readRecords(path)
			if err != nil {
				t.Fatalf("readRecords: %v", err)
			}
			if got := pageTestIDs(withoutSchemaHeader(full)); !reflect.DeepEqual(got, tc.wantIDs) {
				t.Fatalf("readRecords ids = %v, want %v", got, tc.wantIDs)
			}

			page, err := ReadPage(path, "", 1<<20, false)
			if err != nil {
				t.Fatalf("ReadPage: %v", err)
			}
			if got := pageTestIDs(page.Records); !reflect.DeepEqual(got, tc.wantIDs) {
				t.Fatalf("ReadPage ids = %v, want %v", got, tc.wantIDs)
			}

			numbered, err := readNumberedRecords(path)
			if err != nil {
				t.Fatalf("readNumberedRecords: %v", err)
			}
			got := make([]string, 0, len(numbered))
			for _, nr := range numbered {
				got = append(got, nr.record.ID)
			}
			if !reflect.DeepEqual(got, tc.wantIDs) {
				t.Fatalf("readNumberedRecords ids = %v, want %v", got, tc.wantIDs)
			}

			if index := indexByID(withoutSchemaHeader(full)); len(index) != len(tc.wantIDs) {
				t.Fatalf("indexByID has %d entries, want %d (ids collided): %v", len(index), len(tc.wantIDs), tc.wantIDs)
			}
		})
	}
}

// TestNextIDSynthesizedIDsAreDistinct 守护共享辅助函数本身：一旦填入合成 id，
// 它就必须算作已使用，供下一条记录参考。
func TestNextIDSynthesizedIDsAreDistinct(t *testing.T) {
	records := make([]Record, 3)
	for i := range records {
		records[i].ID = nextID(records)
	}
	if got, want := pageTestIDs(records), []string{"n000001", "n000002", "n000003"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("synthesized ids = %v, want %v", got, want)
	}
}
