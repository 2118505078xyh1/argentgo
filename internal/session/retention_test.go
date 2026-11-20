package session

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// markedRecord 构建一条 Data 中带有保留提示的消息记录。
func markedRecord(id string, r Retention) Record {
	data, err := SetRetention([]byte(`{"content":"`+id+`"}`), r)
	if err != nil {
		panic(err)
	}
	return Record{ID: id, Kind: KindMessage, Data: data}
}

// plainRecord 构建一条完全没有保留元数据的消息记录。
func plainRecord(id string) Record {
	return Record{ID: id, Kind: KindMessage, Data: json.RawMessage(`{"content":"` + id + `"}`)}
}

func recordIDs(records []Record) []string {
	out := make([]string, 0, len(records))
	for _, record := range records {
		out = append(out, record.ID)
	}
	return out
}

// normalizedObject 解码 JSON 对象以进行语义比较，忽略
// 保留的保留策略键以及任何无关紧要的格式差异。
func normalizedObject(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("json.Unmarshal(%q): %v", data, err)
	}
	delete(value, retentionKey)
	return value
}

func TestRetentionString(t *testing.T) {
	tests := []struct {
		r    Retention
		want string
	}{
		{RetainDefault, "default"},
		{RetainAlways, "always"},
		{RetainInThread, "in_thread"},
		{RetainInChildren, "in_children"},
		{Retention(99), "Retention(99)"},
	}
	for _, tt := range tests {
		if got := tt.r.String(); got != tt.want {
			t.Errorf("Retention(%d).String() = %q, want %q", uint8(tt.r), got, tt.want)
		}
	}
}

func TestRetentionLifetimes(t *testing.T) {
	tests := []struct {
		r          Retention
		compaction bool
		children   bool
	}{
		{RetainDefault, false, false},
		{RetainAlways, true, true},
		{RetainInThread, true, false},
		{RetainInChildren, false, true},
		{Retention(99), false, false},
	}
	for _, tt := range tests {
		if got := tt.r.SurvivesCompaction(); got != tt.compaction {
			t.Errorf("Retention(%d).SurvivesCompaction() = %v, want %v", uint8(tt.r), got, tt.compaction)
		}
		if got := tt.r.SurvivesChildren(); got != tt.children {
			t.Errorf("Retention(%d).SurvivesChildren() = %v, want %v", uint8(tt.r), got, tt.children)
		}
	}
}

func TestSetGetRetentionRoundtrip(t *testing.T) {
	original := []byte(`{"content":"hello","count":7,"ok":true}`)
	for _, r := range []Retention{RetainDefault, RetainAlways, RetainInThread, RetainInChildren} {
		t.Run(r.String(), func(t *testing.T) {
			out, err := SetRetention(original, r)
			if err != nil {
				t.Fatalf("SetRetention: %v", err)
			}
			if got := GetRetention(out); got != r {
				t.Fatalf("GetRetention = %v, want %v", got, r)
			}
			if got, want := normalizedObject(t, out), normalizedObject(t, original); !reflect.DeepEqual(got, want) {
				t.Fatalf("other fields changed: got %v, want %v", got, want)
			}
		})
	}
}

func TestSetRetentionPreservesComplexFields(t *testing.T) {
	original := []byte(`{"nested":{"a":[1,2,3],"b":null},"ratio":1.25,"标签":"值","list":[true,false]}`)
	out, err := SetRetention(original, RetainAlways)
	if err != nil {
		t.Fatalf("SetRetention: %v", err)
	}
	if got, want := normalizedObject(t, out), normalizedObject(t, original); !reflect.DeepEqual(got, want) {
		t.Fatalf("complex fields changed: got %v, want %v", got, want)
	}
	if got := GetRetention(out); got != RetainAlways {
		t.Fatalf("GetRetention = %v, want RetainAlways", got)
	}
}

func TestSetRetentionEmptyData(t *testing.T) {
	for _, original := range [][]byte{nil, {}, []byte("   "), []byte("null")} {
		out, err := SetRetention(original, RetainInChildren)
		if err != nil {
			t.Fatalf("SetRetention(%q): %v", original, err)
		}
		if got := GetRetention(out); got != RetainInChildren {
			t.Fatalf("GetRetention(%q) = %v, want RetainInChildren", out, got)
		}
	}
}

func TestSetRetentionRejectsNonObjectData(t *testing.T) {
	for _, original := range []string{`{bad`, `[1,2]`, `"text"`, `42`, `true`} {
		out, err := SetRetention([]byte(original), RetainAlways)
		if err == nil {
			t.Errorf("SetRetention(%s) returned no error, got %s", original, out)
		}
		if out != nil {
			t.Errorf("SetRetention(%s) returned data %s on error, want nil", original, out)
		}
	}
}

func TestGetRetentionDefaults(t *testing.T) {
	for _, data := range [][]byte{
		nil,
		{},
		[]byte("   "),
		[]byte(`{bad`),
		[]byte(`[1,2]`),
		[]byte(`"text"`),
		[]byte(`{}`),
		[]byte(`{"_retention":"bogus"}`),
		[]byte(`{"_retention":5}`),
		[]byte(`{"_retention":null}`),
		[]byte(`{"content":"no hint"}`),
	} {
		if got := GetRetention(data); got != RetainDefault {
			t.Errorf("GetRetention(%q) = %v, want RetainDefault", data, got)
		}
	}
}

func TestGetRetentionAcceptsCaseVariants(t *testing.T) {
	data := []byte(`{"_retention":" ALWAYS "}`)
	if got := GetRetention(data); got != RetainAlways {
		t.Fatalf("GetRetention(%q) = %v, want RetainAlways", data, got)
	}
}

func TestSetRetentionLargeDataBounded(t *testing.T) {
	blob := strings.Repeat("x", 128<<10)
	original, err := json.Marshal(map[string]string{"blob": blob, "keep": "v"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := SetRetention(original, RetainInThread)
	if err != nil {
		t.Fatalf("SetRetention: %v", err)
	}
	if got := GetRetention(out); got != RetainInThread {
		t.Fatalf("GetRetention = %v, want RetainInThread", got)
	}
	// 该提示只增加一个很小的字段；载荷不会被复制或截断。
	if len(out) > len(original)+128 {
		t.Fatalf("bounded data violated: original %d bytes, output %d bytes", len(original), len(out))
	}
	var decoded map[string]string
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if decoded["blob"] != blob || decoded["keep"] != "v" {
		t.Fatalf("large payload changed: blob len %d, keep %q", len(decoded["blob"]), decoded["keep"])
	}
}

func TestSelectForCompaction(t *testing.T) {
	records := []Record{
		markedRecord("a", RetainAlways),
		markedRecord("b", RetainInThread),
		markedRecord("c", RetainInChildren),
		markedRecord("d", RetainDefault),
		markedRecord("e", RetainInThread),
		markedRecord("f", RetainAlways),
	}
	tests := []struct {
		name   string
		budget int
		want   []string
	}{
		{"unlimited_zero", 0, []string{"a", "b", "e", "f"}},
		{"unlimited_negative", -1, []string{"a", "b", "e", "f"}},
		{"cap_one", 1, []string{"f"}},
		{"cap_two", 2, []string{"e", "f"}},
		{"cap_above", 10, []string{"a", "b", "e", "f"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := recordIDs(SelectForCompaction(records, tt.budget))
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("SelectForCompaction(budget=%d) = %v, want %v", tt.budget, got, tt.want)
			}
		})
	}
}

func TestSelectForChildren(t *testing.T) {
	records := []Record{
		markedRecord("a", RetainAlways),
		markedRecord("b", RetainInThread),
		markedRecord("c", RetainInChildren),
		markedRecord("d", RetainDefault),
		markedRecord("e", RetainInThread),
		markedRecord("f", RetainAlways),
	}
	got := recordIDs(SelectForChildren(records))
	want := []string{"a", "c", "f"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SelectForChildren = %v, want %v", got, want)
	}
}

func TestRetentionNoMetadataRegression(t *testing.T) {
	records := []Record{
		plainRecord("p1"),
		plainRecord("p2"),
		markedRecord("p3", RetainDefault),
	}
	for _, record := range records {
		if got := GetRetention(record.Data); got != RetainDefault {
			t.Fatalf("GetRetention(%s) = %v, want RetainDefault", record.ID, got)
		}
	}
	if got := SelectForCompaction(records, 0); len(got) != 0 {
		t.Fatalf("SelectForCompaction = %v, want empty", recordIDs(got))
	}
	if got := SelectForChildren(records); len(got) != 0 {
		t.Fatalf("SelectForChildren = %v, want empty", recordIDs(got))
	}
}
