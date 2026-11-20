package workflow

import (
	"reflect"
	"testing"
)

func TestRecoveryStep(t *testing.T) {
	tests := []struct {
		name           string
		journal        []StepRecord
		current        int
		wantResumeFrom int
		wantSkip       bool
	}{
		{
			name:           "empty journal resumes at current",
			journal:        nil,
			current:        0,
			wantResumeFrom: 0,
			wantSkip:       false,
		},
		{
			name:           "empty journal with nonzero current",
			journal:        []StepRecord{},
			current:        3,
			wantResumeFrom: 3,
			wantSkip:       false,
		},
		{
			name: "partial completion skips only the completed step",
			journal: []StepRecord{
				{Index: 0, Name: "scan", Status: StatusOK},
			},
			current:        0,
			wantResumeFrom: 1,
			wantSkip:       true,
		},
		{
			name: "partial completion before current leaves current unchanged",
			journal: []StepRecord{
				{Index: 0, Name: "scan", Status: StatusOK},
			},
			current:        1,
			wantResumeFrom: 1,
			wantSkip:       false,
		},
		{
			name: "all completed resumes past the last step",
			journal: []StepRecord{
				{Index: 0, Name: "a", Status: StatusOK},
				{Index: 1, Name: "b", Status: StatusOK},
				{Index: 2, Name: "c", Status: StatusOK},
			},
			current:        0,
			wantResumeFrom: 3,
			wantSkip:       true,
		},
		{
			name: "out of order journal is indexed by Index not slice position",
			journal: []StepRecord{
				{Index: 2, Name: "c", Status: StatusOK},
				{Index: 0, Name: "a", Status: StatusOK},
				{Index: 1, Name: "b", Status: StatusOK},
			},
			current:        0,
			wantResumeFrom: 3,
			wantSkip:       true,
		},
		{
			name: "out of order journal with gap resumes at the gap",
			journal: []StepRecord{
				{Index: 2, Name: "c", Status: StatusOK},
				{Index: 0, Name: "a", Status: StatusOK},
			},
			current:        0,
			wantResumeFrom: 1,
			wantSkip:       true,
		},
		{
			name: "CJK step names are carried without affecting the decision",
			journal: []StepRecord{
				{Index: 1, Name: "审阅", Status: StatusOK},
				{Index: 0, Name: "扫描文件", Status: StatusOK},
				{Index: 2, Name: "生成报告", Status: StatusFailed},
			},
			current:        0,
			wantResumeFrom: 2,
			wantSkip:       true,
		},
		{
			name: "failed and skipped records are retried",
			journal: []StepRecord{
				{Index: 0, Name: "a", Status: StatusOK},
				{Index: 1, Name: "b", Status: StatusFailed},
				{Index: 2, Name: "c", Status: StatusSkipped},
			},
			current:        1,
			wantResumeFrom: 1,
			wantSkip:       false,
		},
		{
			name: "a later success makes a retried step completed",
			journal: []StepRecord{
				{Index: 0, Name: "a", Status: StatusFailed},
				{Index: 0, Name: "a", Status: StatusOK},
			},
			current:        0,
			wantResumeFrom: 1,
			wantSkip:       true,
		},
		{
			name: "current beyond the recorded steps is left untouched",
			journal: []StepRecord{
				{Index: 0, Name: "a", Status: StatusOK},
			},
			current:        5,
			wantResumeFrom: 5,
			wantSkip:       false,
		},
		{
			name: "negative current is treated as zero",
			journal: []StepRecord{
				{Index: 0, Name: "a", Status: StatusOK},
			},
			current:        -3,
			wantResumeFrom: 1,
			wantSkip:       true,
		},
		{
			name: "records with negative index are ignored",
			journal: []StepRecord{
				{Index: -1, Name: "bad", Status: StatusOK},
			},
			current:        0,
			wantResumeFrom: 0,
			wantSkip:       false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// RecoveryStep 是纯函数：保留一份副本，使日志的修改能被发现，即使返回的决策正确。
			// 能被发现，即使返回的决策正确。
			var before []StepRecord
			if tc.journal != nil {
				before = append([]StepRecord{}, tc.journal...)
			}
			resumeFrom, skip := RecoveryStep(tc.journal, tc.current)
			if resumeFrom != tc.wantResumeFrom || skip != tc.wantSkip {
				t.Fatalf("RecoveryStep() = (%d, %v), want (%d, %v)",
					resumeFrom, skip, tc.wantResumeFrom, tc.wantSkip)
			}
			if !reflect.DeepEqual(tc.journal, before) {
				t.Fatalf("RecoveryStep mutated the journal: got %+v, want %+v", tc.journal, before)
			}
		})
	}
}
