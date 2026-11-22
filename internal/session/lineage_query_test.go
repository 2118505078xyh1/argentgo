package session

import (
	"fmt"
	"reflect"
	"testing"
)

// linkAttempt 构造一条携带分叉血缘的尝试记录，便于表驱动测试。
func linkAttempt(sessionID, recordID string) Attempt {
	return Attempt{Reason: AttemptRetried, SourceSession: sessionID, SourceRecord: recordID}
}

// TestTraceLineageSingleLevel 覆盖单层血缘：单个来源、同一来源会话的多条记录，
// 以及没有记录 id 的链接。同一来源会话的记录按 id 字典序排列。
func TestTraceLineageSingleLevel(t *testing.T) {
	tests := []struct {
		name     string
		attempts []Attempt
		want     []LineageLink
	}{
		{"单条", []Attempt{linkAttempt("sess-a", "n000001")},
			[]LineageLink{{"sess-a", "n000001"}}},
		{"同会话多条按记录 id 排序", []Attempt{
			linkAttempt("sess-a", "n000009"),
			linkAttempt("sess-a", "n000002"),
			linkAttempt("sess-a", "n000005"),
		}, []LineageLink{
			{"sess-a", "n000002"},
			{"sess-a", "n000005"},
			{"sess-a", "n000009"},
		}},
		{"记录 id 为空", []Attempt{linkAttempt("sess-a", "")},
			[]LineageLink{{"sess-a", ""}}},
		{"空输入", nil, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := TraceLineage(tc.attempts, 0)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("TraceLineage = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestTraceLineageMultiLevel 覆盖多层血缘：记录 id 同时作为下一跳的来源会话键，
// 串成 s0 -> s1 -> s2 -> s3 的链，输出按深度从小到大。
func TestTraceLineageMultiLevel(t *testing.T) {
	attempts := []Attempt{
		linkAttempt("s2", "s3"),
		linkAttempt("s0", "s1"),
		linkAttempt("s1", "s2"),
	}
	want := []LineageLink{
		{"s0", "s1"},
		{"s1", "s2"},
		{"s2", "s3"},
	}
	got := TraceLineage(attempts, 0)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("TraceLineage = %+v, want %+v", got, want)
	}
}

// TestTraceLineageCycle 覆盖环防护：链成环（s0->s1->s2->s0）且含自环（s3->s3）
// 时必须终止，每条链接只输出一次，不会死循环。
func TestTraceLineageCycle(t *testing.T) {
	attempts := []Attempt{
		linkAttempt("s0", "s1"),
		linkAttempt("s1", "s2"),
		linkAttempt("s2", "s0"),
		linkAttempt("s3", "s3"),
	}
	got := TraceLineage(attempts, 0)

	// 纯环没有自然根，会退化到全根展开：所有链接各出现一次。
	wantSet := map[LineageLink]bool{
		{"s0", "s1"}: true,
		{"s1", "s2"}: true,
		{"s2", "s0"}: true,
		{"s3", "s3"}: true,
	}
	if len(got) != len(wantSet) {
		t.Fatalf("len = %d, want %d: %+v", len(got), len(wantSet), got)
	}
	for _, link := range got {
		if !wantSet[link] {
			t.Fatalf("unexpected link %+v in %+v", link, got)
		}
		delete(wantSet, link)
	}
	if len(wantSet) != 0 {
		t.Fatalf("missing links: %+v", wantSet)
	}
}

// TestTraceLineageDedup 覆盖去重：完全相同的尝试重复多次、且同一来源会话中
// 记录 id 相同时，只输出一条链接。
func TestTraceLineageDedup(t *testing.T) {
	attempts := []Attempt{
		linkAttempt("s0", "n1"),
		linkAttempt("s0", "n1"),
		linkAttempt("s0", "n1"),
		linkAttempt("s1", "n2"),
		linkAttempt("s1", "n2"),
	}
	got := TraceLineage(attempts, 0)
	want := []LineageLink{{"s0", "n1"}, {"s1", "n2"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("TraceLineage = %+v, want %+v", got, want)
	}
}

// TestTraceLineageMaxDepth 覆盖 maxDepth 截断与默认值：20 跳链在不同 limit 下
// 只输出前若干层；maxDepth<=0 时使用默认值 16。
func TestTraceLineageMaxDepth(t *testing.T) {
	const chain = 20
	attempts := make([]Attempt, 0, chain)
	for i := 0; i < chain; i++ {
		attempts = append(attempts, linkAttempt(fmt.Sprintf("s%02d", i), fmt.Sprintf("s%02d", i+1)))
	}
	for _, tc := range []struct {
		name     string
		maxDepth int
		want     int
	}{
		{"limit 1", 1, 1},
		{"limit 3", 3, 3},
		{"limit 16 以上", 100, chain},
		{"零值用默认 16", 0, defaultLineageDepth},
		{"负值用默认 16", -5, defaultLineageDepth},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := TraceLineage(attempts, tc.maxDepth)
			if len(got) != tc.want {
				t.Fatalf("len = %d, want %d: %+v", len(got), tc.want, got)
			}
			// 截断后仍是最浅的若干层，按深度顺序排列。
			for i, link := range got {
				wantID := fmt.Sprintf("s%02d", i)
				if link.SessionID != wantID {
					t.Fatalf("link %d = %+v, want SessionID %q", i, link, wantID)
				}
			}
		})
	}
}

// TestTraceLineageCJK 覆盖中文 id：会话与记录 id 原样保留，不被转义或切坏。
func TestTraceLineageCJK(t *testing.T) {
	attempts := []Attempt{
		linkAttempt("会话-寒弦", "记录-甲"),
		linkAttempt("记录-甲", "记录-乙"),
	}
	want := []LineageLink{
		{"会话-寒弦", "记录-甲"},
		{"记录-甲", "记录-乙"},
	}
	got := TraceLineage(attempts, 0)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("TraceLineage = %+v, want %+v", got, want)
	}
}

// TestTraceLineageNoInputMutation 确认只读入参：调用前后 attempts 内容一致。
func TestTraceLineageNoInputMutation(t *testing.T) {
	attempts := []Attempt{
		linkAttempt("s0", "n1"),
		linkAttempt("s1", "n2"),
		linkAttempt("s0", "n1"),
	}
	before := append([]Attempt(nil), attempts...)
	_ = TraceLineage(attempts, 0)
	if !reflect.DeepEqual(attempts, before) {
		t.Fatalf("attempts mutated: got %+v, want %+v", attempts, before)
	}
}

// TestTraceLineageDeterministic 确认确定性：相同输入（不同顺序、重复调用）总得到
// 相同输出，且返回的切片彼此独立（修改一个不影响另一个）。
func TestTraceLineageDeterministic(t *testing.T) {
	attempts := []Attempt{
		linkAttempt("s2", "s3"),
		linkAttempt("s0", "s1"),
		linkAttempt("s1", "s2"),
		linkAttempt("s0", "s1"), // 重复项
	}
	first := TraceLineage(attempts, 0)
	second := TraceLineage(attempts, 0)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("unstable: first=%+v second=%+v", first, second)
	}

	// 打乱输入顺序不应改变输出（根按字典序，边按 id 排序）。
	shuffled := []Attempt{attempts[2], attempts[0], attempts[1], attempts[3]}
	third := TraceLineage(shuffled, 0)
	if !reflect.DeepEqual(third, first) {
		t.Fatalf("order-dependent: shuffled=%+v first=%+v", third, first)
	}

	// 返回切片独立：修改 first 不应影响 second。
	if len(first) > 0 {
		first[0].RecordID = "mutated"
		if second[0].RecordID == "mutated" {
			t.Fatal("callers share the same result slice")
		}
	}
}

// TestTraceLineageSkipsNoSource 确认没有来源会话的 Attempt 被跳过，
// 而不影响其余链接的追溯。
func TestTraceLineageSkipsNoSource(t *testing.T) {
	attempts := []Attempt{
		{Reason: AttemptFailed}, // SourceSession 为空
		linkAttempt("s0", "n1"),
		{Reason: AttemptCancelled, SourceRecord: "orphan"}, // 只有记录 id，没有会话
	}
	got := TraceLineage(attempts, 0)
	want := []LineageLink{{"s0", "n1"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("TraceLineage = %+v, want %+v", got, want)
	}
}
