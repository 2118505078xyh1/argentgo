package session

import (
	"encoding/json"
	"reflect"
	"testing"
)

// qrRec 构造一条仅用于查询测试的记录；data 为 JSON 文本片段。
func qrRec(id, parent, kind, data string) Record {
	return Record{ID: id, Parent: parent, Kind: kind, Data: json.RawMessage(data)}
}

// cloneRecords 制作入参的深拷贝，用于断言查询函数不会修改入参。
func cloneRecords(records []Record) []Record {
	if records == nil {
		return nil
	}
	out := make([]Record, len(records))
	for i, r := range records {
		out[i] = r
		if r.Data != nil {
			out[i].Data = append(json.RawMessage(nil), r.Data...)
		}
	}
	return out
}

func TestFilterByKind(t *testing.T) {
	base := []Record{
		qrRec("a", "", KindHeader, `{"title":"根"}`),
		qrRec("b", "a", KindUser, `"你好，世界"`),
		qrRec("c", "b", KindAssistant, `"收到"`),
		qrRec("d", "c", KindTool, `"工具"`),
		qrRec("e", "d", "自定义", `"自定"`),
	}
	tests := []struct {
		name   string
		input  []Record
		kinds  []string
		wantID []string
	}{
		{
			name:   "多类型按原顺序过滤",
			input:  base,
			kinds:  []string{KindUser, KindTool},
			wantID: []string{"b", "d"},
		},
		{
			name:   "单类型",
			input:  base,
			kinds:  []string{KindAssistant},
			wantID: []string{"c"},
		},
		{
			name:   "未知类型返回空",
			input:  base,
			kinds:  []string{"不存在"},
			wantID: []string{},
		},
		{
			name:   "无参数返回空",
			input:  base,
			kinds:  nil,
			wantID: []string{},
		},
		{
			name:   "CJK 自定义类型",
			input:  base,
			kinds:  []string{"自定义"},
			wantID: []string{"e"},
		},
		{
			name:   "空记录集",
			input:  []Record{},
			kinds:  []string{KindUser},
			wantID: []string{},
		},
		{
			name:   "nil 记录集",
			input:  nil,
			kinds:  []string{KindUser},
			wantID: []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := cloneRecords(tt.input)
			got := FilterByKind(tt.input, tt.kinds...)
			gotIDs := make([]string, 0, len(got))
			for _, r := range got {
				gotIDs = append(gotIDs, r.ID)
			}
			if !reflect.DeepEqual(gotIDs, tt.wantID) {
				t.Fatalf("id = %v, 期望 %v", gotIDs, tt.wantID)
			}
			if !reflect.DeepEqual(tt.input, snapshot) {
				t.Fatalf("入参被修改: got %v, 期望 %v", tt.input, snapshot)
			}
		})
	}
}

func TestFilterByKindReturnsIndependentSlice(t *testing.T) {
	input := []Record{qrRec("a", "", KindUser, `"甲"`)}
	got := FilterByKind(input, KindUser)
	got[0].ID = "已改"
	got = append(got, qrRec("b", "", KindUser, `"乙"`))
	if input[0].ID != "a" || len(input) != 1 {
		t.Fatalf("返回切片与入参共享底层数组: input = %v", input)
	}
}

func TestAncestors(t *testing.T) {
	chain := []Record{
		qrRec("a", "", KindHeader, `"根"`),
		qrRec("b", "a", KindUser, `"父"`),
		qrRec("c", "b", KindAssistant, `"子"`),
		qrRec("d", "c", KindTool, `"孙"`),
	}
	cycle := []Record{
		qrRec("x", "y", KindUser, `"甲"`),
		qrRec("y", "z", KindUser, `"乙"`),
		qrRec("z", "x", KindUser, `"丙"`),
	}
	selfCycle := []Record{qrRec("s", "s", KindUser, `"自环"`)}
	missingParent := []Record{
		qrRec("m", "不存在", KindUser, `"父不在集内"`),
	}
	cjk := []Record{
		qrRec("根", "", KindHeader, `"根节点"`),
		qrRec("父", "根", KindUser, `"父亲"`),
		qrRec("子", "父", KindAssistant, `"孩子"`),
	}
	tests := []struct {
		name   string
		input  []Record
		id     string
		wantID []string
	}{
		{
			name:   "多级祖先从近到远且不含自身",
			input:  chain,
			id:     "d",
			wantID: []string{"c", "b", "a"},
		},
		{
			name:   "根记录无祖先",
			input:  chain,
			id:     "a",
			wantID: []string{},
		},
		{
			name:   "id 不存在返回空",
			input:  chain,
			id:     "无",
			wantID: []string{},
		},
		{
			name:   "环被截断不重复",
			input:  cycle,
			id:     "x",
			wantID: []string{"y", "z"},
		},
		{
			name:   "自环返回空",
			input:  selfCycle,
			id:     "s",
			wantID: []string{},
		},
		{
			name:   "父不在集内时停止",
			input:  missingParent,
			id:     "m",
			wantID: []string{},
		},
		{
			name:   "CJK id 与数据",
			input:  cjk,
			id:     "子",
			wantID: []string{"父", "根"},
		},
		{
			name:   "空记录集",
			input:  []Record{},
			id:     "a",
			wantID: []string{},
		},
		{
			name:   "nil 记录集",
			input:  nil,
			id:     "a",
			wantID: []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := cloneRecords(tt.input)
			got := Ancestors(tt.input, tt.id)
			gotIDs := make([]string, 0, len(got))
			for _, r := range got {
				gotIDs = append(gotIDs, r.ID)
			}
			if !reflect.DeepEqual(gotIDs, tt.wantID) {
				t.Fatalf("id = %v, 期望 %v", gotIDs, tt.wantID)
			}
			if !reflect.DeepEqual(tt.input, snapshot) {
				t.Fatalf("入参被修改: got %v, 期望 %v", tt.input, snapshot)
			}
		})
	}
}

func TestAncestorsReturnsIndependentSlice(t *testing.T) {
	input := []Record{
		qrRec("a", "", KindHeader, `"根"`),
		qrRec("b", "a", KindUser, `"父"`),
	}
	got := Ancestors(input, "b")
	got[0].ID = "已改"
	if input[0].ID != "a" {
		t.Fatalf("返回切片与入参共享元素: input = %v", input)
	}
}

func TestDescendants(t *testing.T) {
	// 文件顺序刻意与树遍历顺序不同，以验证结果按出现顺序稳定排列。
	tree := []Record{
		qrRec("root", "", KindHeader, `"根"`),
		qrRec("b", "root", KindUser, `"乙"`),
		qrRec("c", "root", KindAssistant, `"丙"`),
		qrRec("d", "b", KindTool, `"丁"`),
		qrRec("e", "c", KindTool, `"戊"`),
		qrRec("unrelated", "", KindUser, `"无关"`),
	}
	cycle := []Record{
		qrRec("x", "y", KindUser, `"甲"`),
		qrRec("y", "x", KindUser, `"乙"`),
		qrRec("z", "y", KindUser, `"丙"`),
	}
	selfCycle := []Record{qrRec("s", "s", KindUser, `"自环"`)}
	cjk := []Record{
		qrRec("根", "", KindHeader, `"根"`),
		qrRec("子一", "根", KindUser, `"你好"`),
		qrRec("子二", "根", KindAssistant, `"世界"`),
		qrRec("孙", "子一", KindTool, `"孙辈"`),
	}
	tests := []struct {
		name   string
		input  []Record
		id     string
		wantID []string
	}{
		{
			name:   "树的所有后代按出现顺序",
			input:  tree,
			id:     "root",
			wantID: []string{"b", "c", "d", "e"},
		},
		{
			name:   "叶子无后代",
			input:  tree,
			id:     "d",
			wantID: []string{},
		},
		{
			name:   "中间节点仅含其子树",
			input:  tree,
			id:     "b",
			wantID: []string{"d"},
		},
		{
			name:   "id 不存在返回空",
			input:  tree,
			id:     "无",
			wantID: []string{},
		},
		{
			name:   "环被截断且不含自身",
			input:  cycle,
			id:     "x",
			wantID: []string{"y", "z"},
		},
		{
			name:   "自环返回空",
			input:  selfCycle,
			id:     "s",
			wantID: []string{},
		},
		{
			name:   "CJK id 与数据",
			input:  cjk,
			id:     "根",
			wantID: []string{"子一", "子二", "孙"},
		},
		{
			name:   "空记录集",
			input:  []Record{},
			id:     "root",
			wantID: []string{},
		},
		{
			name:   "nil 记录集",
			input:  nil,
			id:     "root",
			wantID: []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := cloneRecords(tt.input)
			got := Descendants(tt.input, tt.id)
			gotIDs := make([]string, 0, len(got))
			for _, r := range got {
				gotIDs = append(gotIDs, r.ID)
			}
			if !reflect.DeepEqual(gotIDs, tt.wantID) {
				t.Fatalf("id = %v, 期望 %v", gotIDs, tt.wantID)
			}
			if !reflect.DeepEqual(tt.input, snapshot) {
				t.Fatalf("入参被修改: got %v, 期望 %v", tt.input, snapshot)
			}
		})
	}
}

func TestDescendantsReturnsIndependentSlice(t *testing.T) {
	input := []Record{
		qrRec("a", "", KindHeader, `"根"`),
		qrRec("b", "a", KindUser, `"子"`),
	}
	got := Descendants(input, "a")
	got[0].ID = "已改"
	if input[1].ID != "b" {
		t.Fatalf("返回切片与入参共享元素: input = %v", input)
	}
}

func TestFirstMatch(t *testing.T) {
	base := []Record{
		qrRec("a", "", KindHeader, `"根"`),
		qrRec("b", "a", KindUser, `"你好，世界"`),
		qrRec("c", "b", KindAssistant, `"收到"`),
		qrRec("d", "c", KindUser, `"再次"`),
	}
	byKind := func(kind string) func(Record) bool {
		return func(r Record) bool { return r.Kind == kind }
	}
	tests := []struct {
		name      string
		input     []Record
		pred      func(Record) bool
		wantID    string
		wantFound bool
	}{
		{
			name:      "命中第一条",
			input:     base,
			pred:      byKind(KindUser),
			wantID:    "b",
			wantFound: true,
		},
		{
			name:      "按条件命中中间记录",
			input:     base,
			pred:      func(r Record) bool { return r.ID == "c" },
			wantID:    "c",
			wantFound: true,
		},
		{
			name:      "未命中",
			input:     base,
			pred:      byKind("不存在"),
			wantID:    "",
			wantFound: false,
		},
		{
			name:      "nil 谓词",
			input:     base,
			pred:      nil,
			wantID:    "",
			wantFound: false,
		},
		{
			name:      "CJK 数据命中",
			input:     base,
			pred:      func(r Record) bool { return string(r.Data) == `"你好，世界"` },
			wantID:    "b",
			wantFound: true,
		},
		{
			name:      "空记录集",
			input:     []Record{},
			pred:      byKind(KindUser),
			wantID:    "",
			wantFound: false,
		},
		{
			name:      "nil 记录集",
			input:     nil,
			pred:      byKind(KindUser),
			wantID:    "",
			wantFound: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := cloneRecords(tt.input)
			got, found := FirstMatch(tt.input, tt.pred)
			if found != tt.wantFound {
				t.Fatalf("found = %v, 期望 %v", found, tt.wantFound)
			}
			if got.ID != tt.wantID {
				t.Fatalf("id = %q, 期望 %q", got.ID, tt.wantID)
			}
			if !tt.wantFound && !reflect.DeepEqual(got, Record{}) {
				t.Fatalf("未命中时应返回零值，得到 %v", got)
			}
			if !reflect.DeepEqual(tt.input, snapshot) {
				t.Fatalf("入参被修改: got %v, 期望 %v", tt.input, snapshot)
			}
		})
	}
}
