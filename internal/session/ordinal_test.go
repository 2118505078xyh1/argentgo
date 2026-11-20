package session

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// stamped 构建一条在 Data 信封中带有显式序号的消息记录。
func stamped(id string, seq int64) Record {
	data, err := SetSeq([]byte(`{"content":"`+id+`"}`), seq)
	if err != nil {
		panic(err)
	}
	return Record{ID: id, Kind: KindMessage, Data: data}
}

// unstamped 构建一条完全没有序号元数据的旧消息记录。
func unstamped(id string) Record {
	return Record{ID: id, Kind: KindMessage, Data: json.RawMessage(`{"content":"` + id + `"}`)}
}

func ordinalIDs(records []Record) []string {
	if len(records) == 0 {
		return nil
	}
	out := make([]string, 0, len(records))
	for _, record := range records {
		out = append(out, record.ID)
	}
	return out
}

// normalizedSeqObject 解码 JSON 对象以进行语义比较，忽略
// 保留的序号键以及任何无关紧要的格式差异。
func normalizedSeqObject(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("json.Unmarshal(%q): %v", data, err)
	}
	delete(value, seqKey)
	return value
}

func TestSetGetSeqRoundtrip(t *testing.T) {
	original := []byte(`{"content":"hello","count":7,"ok":true}`)
	for _, seq := range []int64{0, 1, 42, 1 << 40, int64(1<<63 - 1)} {
		out, err := SetSeq(original, seq)
		if err != nil {
			t.Fatalf("SetSeq(%d): %v", seq, err)
		}
		got, ok := GetSeq(out)
		if !ok || got != seq {
			t.Fatalf("GetSeq = (%d,%v), want (%d,true)", got, ok, seq)
		}
		if want := normalizedSeqObject(t, original); !reflect.DeepEqual(normalizedSeqObject(t, out), want) {
			t.Fatalf("other fields changed for seq %d: got %v, want %v", seq, normalizedSeqObject(t, out), want)
		}
	}
}

func TestSetSeqPreservesComplexFields(t *testing.T) {
	original := []byte(`{"nested":{"a":[1,2,3],"b":null},"ratio":1.25,"标签":"值","list":[true,false],"_retention":"always"}`)
	out, err := SetSeq(original, 17)
	if err != nil {
		t.Fatalf("SetSeq: %v", err)
	}
	if got, want := normalizedSeqObject(t, out), normalizedSeqObject(t, original); !reflect.DeepEqual(got, want) {
		t.Fatalf("other fields changed: got %v, want %v", got, want)
	}
	// 来自其他特性的保留字段必须原样保留。
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(out, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields[retentionKey]) != `"always"` {
		t.Fatalf("_retention changed: %s", fields[retentionKey])
	}
	if got, ok := GetSeq(out); !ok || got != 17 {
		t.Fatalf("GetSeq = (%d,%v), want (17,true)", got, ok)
	}
}

func TestSetSeqEmptyData(t *testing.T) {
	for _, original := range [][]byte{nil, {}, []byte("   "), []byte("null")} {
		out, err := SetSeq(original, 9)
		if err != nil {
			t.Fatalf("SetSeq(%q): %v", original, err)
		}
		if got, ok := GetSeq(out); !ok || got != 9 {
			t.Fatalf("GetSeq(%q) = (%d,%v), want (9,true)", out, got, ok)
		}
	}
}

func TestSetSeqRejectsInvalidInput(t *testing.T) {
	for _, original := range []string{`{bad`, `[1,2]`, `"text"`, `42`, `true`} {
		out, err := SetSeq([]byte(original), 1)
		if err == nil {
			t.Errorf("SetSeq(%s) returned no error, got %s", original, out)
		}
		if out != nil {
			t.Errorf("SetSeq(%s) returned data %s on error, want nil", original, out)
		}
	}
	if out, err := SetSeq([]byte(`{}`), -1); err == nil || out != nil {
		t.Errorf("SetSeq with negative ordinal = (%s,%v), want (nil,error)", out, err)
	}
}

func TestGetSeqRejectsMalformed(t *testing.T) {
	for _, data := range [][]byte{
		nil,
		{},
		[]byte("   "),
		[]byte(`{bad`),
		[]byte(`[1,2]`),
		[]byte(`"text"`),
		[]byte(`42`),
		[]byte(`true`),
		[]byte(`null`),
		[]byte(`{}`),
		[]byte(`{"_seq":null}`),
		[]byte(`{"_seq":"5"}`),
		[]byte(`{"_seq":1.5}`),
		[]byte(`{"_seq":true}`),
		[]byte(`{"_seq":-1}`),
		[]byte(`{"_seq":9223372036854775808}`), // 超出 int64 范围
		[]byte(`{"content":"no ordinal"}`),
	} {
		if got, ok := GetSeq(data); ok || got != 0 {
			t.Errorf("GetSeq(%q) = (%d,%v), want (0,false)", data, got, ok)
		}
	}
}

func TestSetSeqLargeDataBounded(t *testing.T) {
	blob := strings.Repeat("x", 128<<10)
	original, err := json.Marshal(map[string]string{"blob": blob, "keep": "v"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := SetSeq(original, 5)
	if err != nil {
		t.Fatalf("SetSeq: %v", err)
	}
	if len(out) > len(original)+128 {
		t.Fatalf("bounded data violated: original %d bytes, output %d bytes", len(original), len(out))
	}
	// 按 RawMessage 解码：保留字段 _seq 是数字，而不是字符串。
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if string(decoded["blob"]) != `"`+blob+`"` || string(decoded["keep"]) != `"v"` {
		t.Fatalf("large payload changed: blob len %d, keep %s", len(decoded["blob"]), decoded["keep"])
	}
}

func TestRecordSeqsUsesExplicitAndDerivedOrdinals(t *testing.T) {
	records := []Record{stamped("a", 7), unstamped("b"), stamped("c", 9), unstamped("d")}
	got := RecordSeqs(records)
	want := []int64{7, 1, 9, 3}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RecordSeqs = %v, want %v", got, want)
	}
	if got := RecordSeqs(nil); len(got) != 0 {
		t.Fatalf("RecordSeqs(nil) = %v, want empty", got)
	}
}

func TestNewestBySeqEmpty(t *testing.T) {
	if got := NewestBySeq(nil); !reflect.DeepEqual(got, Record{}) {
		t.Fatalf("NewestBySeq(nil) = %+v, want zero Record", got)
	}
}

func TestNewestBySeqHighestSeqWinsWithTiedTimestamps(t *testing.T) {
	// 三条记录都写入于同一时钟周期：TS 无法排序，
	// 只有序号可以。故意把胜者放在中间。
	ts := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	records := []Record{
		{ID: "a", TS: ts, Kind: KindMessage},
		{ID: "c", TS: ts, Kind: KindMessage},
		{ID: "b", TS: ts, Kind: KindMessage},
	}
	data, err := SetSeq([]byte(`{}`), 3)
	if err != nil {
		t.Fatal(err)
	}
	records[1].Data = data // "c" 的 seq 最高（3）
	data, err = SetSeq([]byte(`{}`), 1)
	if err != nil {
		t.Fatal(err)
	}
	records[0].Data = data
	data, err = SetSeq([]byte(`{}`), 2)
	if err != nil {
		t.Fatal(err)
	}
	records[2].Data = data

	if got := NewestBySeq(records); got.ID != "c" {
		t.Fatalf("NewestBySeq = %q, want %q", got.ID, "c")
	}
}

func TestNewestBySeqSameSecondTimestampsDeterministic(t *testing.T) {
	ts := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	older := stamped("older", 4)
	newer := stamped("newer", 9)
	older.TS, newer.TS = ts, ts

	forward := NewestBySeq([]Record{older, newer})
	reverse := NewestBySeq([]Record{newer, older})
	if forward.ID != "newer" || reverse.ID != "newer" {
		t.Fatalf("same-second tie broke: forward %q, reverse %q, want %q", forward.ID, reverse.ID, "newer")
	}
	// ts 相同但字典序 id 更大的旧记录不能胜出。
	legacy := unstamped("zzz")
	legacy.TS = ts
	if got := NewestBySeq([]Record{legacy, older, newer}); got.ID != "newer" {
		t.Fatalf("legacy record outranked a stamped one: got %q", got.ID)
	}
}

func TestNewestBySeqFallsBackForUnstampedRecords(t *testing.T) {
	ts := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	older := unstamped("older")
	newer := unstamped("newer")
	older.TS, newer.TS = ts.Add(-time.Hour), ts

	if got := NewestBySeq([]Record{older, newer}); got.ID != "newer" {
		t.Fatalf("NewestBySeq by TS = %q, want %q", got.ID, "newer")
	}

	// 时间戳相等时回退到较大的 id，与 Store.List 一致。
	a, z := unstamped("a"), unstamped("z")
	a.TS, z.TS = ts, ts
	if got := NewestBySeq([]Record{a, z}); got.ID != "z" {
		t.Fatalf("NewestBySeq by ID = %q, want %q", got.ID, "z")
	}
	if got := NewestBySeq([]Record{z, a}); got.ID != "z" {
		t.Fatalf("NewestBySeq by ID (reversed) = %q, want %q", got.ID, "z")
	}
}

func TestNewestBySeqTiedSeqFallsBackToTimeAndID(t *testing.T) {
	ts := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	early, late := stamped("z-early", 5), stamped("a-late", 5)
	early.TS, late.TS = ts.Add(-time.Hour), ts
	if got := NewestBySeq([]Record{early, late}); got.ID != "a-late" {
		t.Fatalf("tied seq picked %q, want %q", got.ID, "a-late")
	}
	early.TS = ts
	if got := NewestBySeq([]Record{early, late}); got.ID != "z-early" {
		t.Fatalf("fully tied seq picked %q, want greater id %q", got.ID, "z-early")
	}
}

func TestForkBeforeSeqPrefixAndBoundaries(t *testing.T) {
	records := []Record{stamped("a", 10), stamped("b", 20), stamped("c", 30), stamped("d", 40)}
	tests := []struct {
		name   string
		target int64
		want   []string
	}{
		{"below_first", 10, nil},
		{"negative", -1, nil},
		{"at_second", 20, []string{"a"}},
		{"between", 25, []string{"a", "b"}},
		{"at_last", 40, []string{"a", "b", "c"}},
		{"above_all", 99, []string{"a", "b", "c", "d"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ordinalIDs(ForkBeforeSeq(records, tt.target))
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ForkBeforeSeq(%d) = %v, want %v", tt.target, got, tt.want)
			}
		})
	}
	if got := ForkBeforeSeq(nil, 10); got != nil {
		t.Fatalf("ForkBeforeSeq(nil) = %v, want nil", got)
	}
}

func TestForkBeforeSeqWithMissingSeq(t *testing.T) {
	// 旧记录没有 _seq；它们的数组索引就是有效序号。
	records := []Record{unstamped("a"), unstamped("b"), unstamped("c")}
	tests := []struct {
		target int64
		want   []string
	}{
		{0, nil},
		{1, []string{"a"}},
		{2, []string{"a", "b"}},
		{3, []string{"a", "b", "c"}},
	}
	for _, tt := range tests {
		got := ordinalIDs(ForkBeforeSeq(records, tt.target))
		if !reflect.DeepEqual(got, tt.want) {
			t.Fatalf("ForkBeforeSeq(%d) = %v, want %v", tt.target, got, tt.want)
		}
	}
}

func TestForkBeforeSeqReturnsCopy(t *testing.T) {
	records := []Record{stamped("a", 1), stamped("b", 2)}
	prefix := ForkBeforeSeq(records, 3)
	prefix[0].ID = "mutated"
	if records[0].ID != "a" {
		t.Fatalf("mutating prefix changed the source record to %q", records[0].ID)
	}
}
