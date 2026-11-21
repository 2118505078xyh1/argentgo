package session

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// isoTS 是测试记录使用的固定时间戳，便于断言标记中的 Unix 秒。
var isoTS = time.Date(2026, 11, 21, 8, 0, 0, 0, time.UTC)

func isoRecord(id, parent, kind, data string) Record {
	return Record{ID: id, Parent: parent, Kind: kind, TS: isoTS, Data: json.RawMessage(data)}
}

func failedMarker(attemptID, parent string) AttemptMarker {
	return AttemptMarker{AttemptID: attemptID, ParentID: parent, Outcome: AttemptFailed, TS: isoTS.Unix()}
}

func TestMarkFailure(t *testing.T) {
	tests := []struct {
		name      string
		records   []Record
		attemptID string
		want      []AttemptMarker
	}{
		{
			name: "成功尝试无标记",
			records: []Record{
				isoRecord("n0", "", KindHeader, `{"title":"起点"}`),
				isoRecord("att-1", "n0", KindAttempt, `{"reason":"initial"}`),
				isoRecord("att-1-a", "att-1", KindAssistant, `{"content":"done"}`),
			},
			attemptID: "att-1",
			want:      nil,
		},
		{
			name: "is_error 布尔真",
			records: []Record{
				isoRecord("n0", "", KindHeader, `{}`),
				isoRecord("att-1", "n0", KindAttempt, `{}`),
				isoRecord("att-1-t", "att-1", KindTool, `{"name":"bash","output":"boom","is_error":true}`),
			},
			attemptID: "att-1",
			want:      []AttemptMarker{failedMarker("att-1", "att-1")},
		},
		{
			name: "is_error 字符串真",
			records: []Record{
				isoRecord("n0", "", KindHeader, `{}`),
				isoRecord("att-1", "n0", KindAttempt, `{}`),
				isoRecord("att-1-t", "att-1", KindTool, `{"name":"bash","is_error":"true","output":"boom"}`),
			},
			attemptID: "att-1",
			want:      []AttemptMarker{failedMarker("att-1", "att-1")},
		},
		{
			name: "工具输出文本失败",
			records: []Record{
				isoRecord("n0", "", KindHeader, `{}`),
				isoRecord("att-1", "n0", KindAttempt, `{}`),
				isoRecord("att-1-t", "att-1", KindTool, `{"name":"edit","output":"failed: permission denied"}`),
			},
			attemptID: "att-1",
			want:      []AttemptMarker{failedMarker("att-1", "att-1")},
		},
		{
			name: "工具输出中文失败",
			records: []Record{
				isoRecord("n0", "", KindHeader, `{}`),
				isoRecord("尝试-甲", "n0", KindAttempt, `{}`),
				isoRecord("尝试-甲-工具", "尝试-甲", KindTool, `{"name":"编译","output":"失败: 未定义的符号"}`),
			},
			attemptID: "尝试-甲",
			want:      []AttemptMarker{failedMarker("尝试-甲", "尝试-甲")},
		},
		{
			name: "StopError 拒绝文本",
			records: []Record{
				isoRecord("n0", "", KindHeader, `{}`),
				isoRecord("att-1", "n0", KindAttempt, `{}`),
				isoRecord("att-1-t", "att-1", KindTool,
					`{"name":"bash","output":"security: the bash tool call was denied: policy. This block is intentional and final, not a transient error."}`),
			},
			attemptID: "att-1",
			want:      []AttemptMarker{failedMarker("att-1", "att-1")},
		},
		{
			name: "StopError 否决文本出现在 assistant 文本",
			records: []Record{
				isoRecord("n0", "", KindHeader, `{}`),
				isoRecord("att-1", "n0", KindAttempt, `{}`),
				isoRecord("att-1-a", "att-1", KindAssistant, `{"content":"the tool call was refused by the operator"}`),
			},
			attemptID: "att-1",
			want:      []AttemptMarker{failedMarker("att-1", "att-1")},
		},
		{
			name: "多层父子链归属：子节点 id 不带前缀",
			records: []Record{
				isoRecord("n0", "", KindHeader, `{}`),
				isoRecord("att-1", "n0", KindAttempt, `{}`),
				isoRecord("x1", "att-1", KindTool, `{"name":"bash","is_error":true,"output":"boom"}`),
				isoRecord("x2", "x1", KindAssistant, `{"content":"继续"}`),
			},
			attemptID: "att-1",
			want:      []AttemptMarker{failedMarker("att-1", "att-1")},
		},
		{
			name: "稳定顺序与父 id",
			records: []Record{
				isoRecord("n0", "", KindHeader, `{}`),
				isoRecord("att-1", "n0", KindAttempt, `{}`),
				isoRecord("att-1-t1", "att-1", KindTool, `{"output":"failed: first"}`),
				isoRecord("att-1-a", "att-1-t1", KindAssistant, `{"content":"ok"}`),
				isoRecord("att-1-t2", "att-1-a", KindTool, `{"output":"This block is intentional and final"}`),
			},
			attemptID: "att-1",
			want: []AttemptMarker{
				failedMarker("att-1", "att-1"),
				failedMarker("att-1", "att-1-a"),
			},
		},
		{
			name: "排除其它尝试",
			records: []Record{
				isoRecord("n0", "", KindHeader, `{}`),
				isoRecord("att-1", "n0", KindAttempt, `{}`),
				isoRecord("att-1-t", "att-1", KindTool, `{"is_error":true}`),
				isoRecord("att-2", "n0", KindAttempt, `{}`),
				isoRecord("att-2-t", "att-2", KindTool, `{"is_error":true}`),
			},
			attemptID: "att-1",
			want:      []AttemptMarker{failedMarker("att-1", "att-1")},
		},
		{
			name: "前缀边界不串号",
			records: []Record{
				isoRecord("n0", "", KindHeader, `{}`),
				isoRecord("att-1", "n0", KindAttempt, `{}`),
				isoRecord("att-10-t", "n0", KindTool, `{"is_error":true,"output":"boom"}`),
			},
			attemptID: "att-1",
			want:      nil,
		},
		{
			name: "未知尝试无标记",
			records: []Record{
				isoRecord("n0", "", KindHeader, `{}`),
				isoRecord("att-1", "n0", KindAttempt, `{}`),
				isoRecord("att-1-t", "att-1", KindTool, `{"is_error":true}`),
			},
			attemptID: "不存在",
			want:      nil,
		},
		{
			name:      "空记录流",
			records:   nil,
			attemptID: "att-1",
			want:      nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MarkFailure(tt.records, tt.attemptID)
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("MarkFailure() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestMarkFailureDoesNotMutateInput(t *testing.T) {
	records := []Record{
		isoRecord("n0", "", KindHeader, `{}`),
		isoRecord("att-1", "n0", KindAttempt, `{}`),
		isoRecord("att-1-t", "att-1", KindTool, `{"is_error":true,"output":"boom"}`),
	}
	original := cloneISORecords(records)

	_ = MarkFailure(records, "att-1")

	if !reflect.DeepEqual(records, original) {
		t.Fatalf("MarkFailure mutated input: got %#v, want %#v", records, original)
	}
}

func cloneISORecords(records []Record) []Record {
	out := make([]Record, len(records))
	for i, record := range records {
		out[i] = record
		if record.Data != nil {
			out[i].Data = append(json.RawMessage(nil), record.Data...)
		}
	}
	return out
}

func TestCanDiscardAttempt(t *testing.T) {
	tests := []struct {
		name    string
		markers []AttemptMarker
		id      string
		want    bool
	}{
		{
			name: "全部失败可丢弃",
			markers: []AttemptMarker{
				{AttemptID: "att-1", Outcome: AttemptFailed},
				{AttemptID: "att-1", Outcome: AttemptFailed},
			},
			id:   "att-1",
			want: true,
		},
		{
			name: "混入成功不可丢弃",
			markers: []AttemptMarker{
				{AttemptID: "att-1", Outcome: AttemptFailed},
				{AttemptID: "att-1", Outcome: attemptOutcomeSucceeded},
			},
			id:   "att-1",
			want: false,
		},
		{
			name:    "空标记不可丢弃",
			markers: nil,
			id:      "att-1",
			want:    false,
		},
		{
			name: "只有其它尝试的标记",
			markers: []AttemptMarker{
				{AttemptID: "att-2", Outcome: AttemptFailed},
			},
			id:   "att-1",
			want: false,
		},
		{
			name: "未知结果值不可丢弃",
			markers: []AttemptMarker{
				{AttemptID: "att-1", Outcome: ""},
			},
			id:   "att-1",
			want: false,
		},
		{
			name: "空 id",
			markers: []AttemptMarker{
				{AttemptID: "", Outcome: AttemptFailed},
			},
			id:   "",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CanDiscardAttempt(tt.markers, tt.id); got != tt.want {
				t.Fatalf("CanDiscardAttempt() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBranchPointFor(t *testing.T) {
	tests := []struct {
		name      string
		records   []Record
		attemptID string
		want      string
		wantOK    bool
	}{
		{
			name: "精确尝试根节点的父节点",
			records: []Record{
				isoRecord("n0", "", KindHeader, `{}`),
				isoRecord("n1", "n0", KindUser, `{}`),
				isoRecord("att-1", "n1", KindAttempt, `{}`),
				isoRecord("att-1-t", "att-1", KindTool, `{"is_error":true}`),
			},
			attemptID: "att-1",
			want:      "n1",
			wantOK:    true,
		},
		{
			name: "前缀分组的子树根",
			records: []Record{
				isoRecord("n0", "", KindHeader, `{}`),
				isoRecord("att-1-t1", "n0", KindTool, `{}`),
				isoRecord("att-1-t2", "att-1-t1", KindTool, `{}`),
			},
			attemptID: "att-1",
			want:      "n0",
			wantOK:    true,
		},
		{
			name: "多层父子链的分叉点",
			records: []Record{
				isoRecord("n0", "", KindHeader, `{}`),
				isoRecord("n1", "n0", KindUser, `{}`),
				isoRecord("att-1", "n1", KindAttempt, `{}`),
				isoRecord("x1", "att-1", KindTool, `{}`),
				isoRecord("x2", "x1", KindAssistant, `{}`),
			},
			attemptID: "att-1",
			want:      "n1",
			wantOK:    true,
		},
		{
			name: "CJK id 的分叉点",
			records: []Record{
				isoRecord("根", "", KindHeader, `{}`),
				isoRecord("尝试-甲", "根", KindAttempt, `{}`),
				isoRecord("尝试-甲-工具", "尝试-甲", KindTool, `{"is_error":true}`),
			},
			attemptID: "尝试-甲",
			want:      "根",
			wantOK:    true,
		},
		{
			name: "未知尝试",
			records: []Record{
				isoRecord("n0", "", KindHeader, `{}`),
				isoRecord("att-1", "n0", KindAttempt, `{}`),
			},
			attemptID: "不存在",
			want:      "",
			wantOK:    false,
		},
		{
			name: "尝试根无父节点",
			records: []Record{
				isoRecord("att-1", "", KindAttempt, `{}`),
			},
			attemptID: "att-1",
			want:      "",
			wantOK:    false,
		},
		{
			name:      "空记录流",
			records:   nil,
			attemptID: "att-1",
			want:      "",
			wantOK:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := BranchPointFor(tt.records, tt.attemptID)
			if got != tt.want || ok != tt.wantOK {
				t.Fatalf("BranchPointFor() = (%q, %v), want (%q, %v)", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}
