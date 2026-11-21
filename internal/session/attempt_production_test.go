package session

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// TestDiscardFailedAttemptProductionStructure 锁定生产转录结构下 DiscardFailedAttempt 的行为。
//
// 生产转录由 recordAttempt 在「运行末尾」写入 KindAttempt（reason=failed），
// 它的 parent 指向最后一条消息；失败工具记录因此是 attempt 的祖先而非后代。
// attempt 成员靠 parent 边向下遍历识别，尾部审计结构下扫不到任何失败成员，
// CanDiscardAttempt 为 false，原语必须 fail-closed 返回 ErrAttemptNotDiscardable。
// 本测试防止将来误判该原语「已对生产可用」（R11 可恢复性学习 §2.3）。
func TestDiscardFailedAttemptProductionStructure(t *testing.T) {
	tests := []struct {
		name      string
		records   []Record
		attemptID string
		wantErr   error // nil 表示期望丢弃成功
		// wantOutcomes 为 nil 时不检查 MarkFailure；非 nil 时断言其 Outcome 序列
		// （空切片表示无失败标记），并据此推导 CanDiscardAttempt 的期望值。
		wantOutcomes []string
	}{
		{
			name: "尾部审计：失败工具是祖先，fail-closed",
			records: []Record{
				discardRecord("n000001", "", KindHeader, `{"title":"生产会话"}`),
				discardRecord("n000002", "n000001", KindTitle, `{"title":"生产会话"}`),
				discardRecord("n000003", "n000002", KindUser, `"继续任务"`),
				discardRecord("n000004", "n000003", KindTool, `{"name":"bash","is_error":true,"output":"boom"}`),
				discardRecord("n000005", "n000004", KindTool, `{"name":"edit","is_error":true,"output":"编译失败"}`),
				// recordAttempt 写在运行末尾，parent 指向最后一条消息。
				discardRecord("n000006", "n000005", KindAttempt, `{"reason":"failed"}`),
			},
			attemptID:    "n000006",
			wantErr:      ErrAttemptNotDiscardable,
			wantOutcomes: []string{},
		},
		{
			name: "对照：前置根 + 后代失败工具，丢弃成功",
			records: []Record{
				discardRecord("n000001", "", KindHeader, `{"title":"生产会话"}`),
				discardRecord("n000002", "n000001", KindTitle, `{"title":"生产会话"}`),
				discardRecord("n000003", "n000002", KindUser, `"继续任务"`),
				// 与现有 fixture 一致：KindAttempt 在前，失败工具为后代。
				discardRecord("n000004", "n000003", KindAttempt, `{"reason":"initial"}`),
				discardRecord("n000005", "n000004", KindTool, `{"name":"bash","is_error":true,"output":"boom"}`),
				discardRecord("n000006", "n000005", KindAssistant, `"收尾"`),
			},
			attemptID:    "n000004",
			wantOutcomes: []string{AttemptFailed},
		},
		{
			name: "尾部审计：CJK 命令与工具名仍 fail-closed",
			records: []Record{
				discardRecord("n000001", "", KindHeader, `{"title":"中文会话"}`),
				discardRecord("n000002", "n000001", KindTitle, `{"title":"中文会话"}`),
				discardRecord("n000003", "n000002", KindUser, `"继续任务"`),
				discardRecord("n000004", "n000003", KindTool, `{"name":"编译","is_error":true,"output":"编译失败：未定义的符号"}`),
				discardRecord("n000005", "n000004", KindTool, `{"name":"单元测试","is_error":true,"output":"测试未通过"}`),
				discardRecord("n000006", "n000005", KindAttempt, `{"reason":"failed"}`),
			},
			attemptID:    "n000006",
			wantErr:      ErrAttemptNotDiscardable,
			wantOutcomes: []string{},
		},
		{
			name: "尾部审计：多层父子仍 fail-closed",
			records: []Record{
				discardRecord("n000001", "", KindHeader, `{"title":"多层父子"}`),
				discardRecord("n000002", "n000001", KindTitle, `{"title":"多层父子"}`),
				discardRecord("n000003", "n000002", KindUser, `"开始"`),
				discardRecord("n000004", "n000003", KindAssistant, `"思考"`),
				discardRecord("n000005", "n000004", KindTool, `{"name":"step1","is_error":true,"output":"boom"}`),
				discardRecord("n000006", "n000005", KindAssistant, `"继续思考"`),
				discardRecord("n000007", "n000006", KindTool, `{"name":"step2","is_error":true,"output":"boom2"}`),
				discardRecord("n000008", "n000007", KindAssistant, `"再思考"`),
				discardRecord("n000009", "n000008", KindAttempt, `{"reason":"failed"}`),
			},
			attemptID:    "n000009",
			wantErr:      ErrAttemptNotDiscardable,
			wantOutcomes: []string{},
		},
		{
			name: "前置根：混入 is_error 为 false 的工具导致拒绝",
			records: []Record{
				discardRecord("n000001", "", KindHeader, `{"title":"混入成功"}`),
				discardRecord("n000002", "n000001", KindTitle, `{"title":"混入成功"}`),
				discardRecord("n000003", "n000002", KindUser, `"继续"`),
				discardRecord("n000004", "n000003", KindAttempt, `{"reason":"initial"}`),
				discardRecord("n000005", "n000004", KindTool, `{"name":"bash","is_error":true,"output":"boom"}`),
				// 显式成功结果污染尝试，必须与「尾部结构」区分开地触发拒绝。
				discardRecord("n000006", "n000005", KindTool, `{"name":"verify","is_error":false,"output":"ok"}`),
				discardRecord("n000007", "n000006", KindAssistant, `"收尾"`),
			},
			attemptID:    "n000004",
			wantErr:      ErrAttemptNotDiscardable,
			wantOutcomes: []string{AttemptFailed, attemptOutcomeSucceeded},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// 机制层：先锁定 MarkFailure / CanDiscardAttempt 的可见性，
			// 使 fail-closed 的理由（失败工具是祖先而非后代）也被固化为断言。
			if tc.wantOutcomes != nil {
				markers := MarkFailure(tc.records, tc.attemptID)
				gotOutcomes := make([]string, 0, len(markers))
				for _, marker := range markers {
					gotOutcomes = append(gotOutcomes, marker.Outcome)
				}
				if !reflect.DeepEqual(gotOutcomes, tc.wantOutcomes) {
					t.Fatalf("MarkFailure() outcomes = %#v, want %#v", gotOutcomes, tc.wantOutcomes)
				}
				wantCan := len(tc.wantOutcomes) > 0
				for _, outcome := range tc.wantOutcomes {
					if outcome != AttemptFailed {
						wantCan = false
					}
				}
				if got := CanDiscardAttempt(markers, tc.attemptID); got != wantCan {
					t.Fatalf("CanDiscardAttempt() = %v, want %v", got, wantCan)
				}
			}

			f := newDiscardFixture(t, "prod", tc.records)
			fork, err := f.session.DiscardFailedAttempt(tc.attemptID)

			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("DiscardFailedAttempt() err = %v, want %v", err, tc.wantErr)
				}
				if fork != nil {
					t.Fatal("拒绝时不应返回 fork")
				}
				f.assertSourceUnchanged(t)
				return
			}

			if err != nil {
				t.Fatalf("DiscardFailedAttempt(): %v", err)
			}
			if fork == nil {
				t.Fatal("fork is nil")
			}
			if fork.ID() == f.session.ID() {
				t.Fatal("fork 复用了源会话 id")
			}
			f.assertSourceUnchanged(t)

			// fork 必须干净：不带入失败工具，且只追加一条重试血缘。
			forkRecords, err := fork.Records()
			if err != nil {
				t.Fatalf("fork.Records(): %v", err)
			}
			for _, record := range forkRecords {
				if record.Kind == KindTool {
					t.Fatalf("失败尝试的工具记录被带进 fork: %+v", record)
				}
			}
			attempts, err := fork.Attempts()
			if err != nil {
				t.Fatalf("fork.Attempts(): %v", err)
			}
			if len(attempts) != 1 || attempts[0].Reason != AttemptRetried || attempts[0].SourceRecord != tc.attemptID {
				t.Fatalf("fork 血缘 = %+v, want 单条 reason=%q source=%q", attempts, AttemptRetried, tc.attemptID)
			}
			branches, err := Branches(fork.path())
			if err != nil {
				t.Fatalf("Branches(fork): %v", err)
			}
			found := false
			for _, branch := range branches {
				if branch.Kind == KindAttempt && branch.SourceSession == f.session.ID() &&
					branch.SourceRecord == tc.attemptID && strings.Contains(branch.Label, AttemptRetried) {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("血缘记录未出现在 Branches 中: %+v", branches)
			}
		})
	}
}
