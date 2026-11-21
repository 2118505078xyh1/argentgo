package eval

import (
	"reflect"
	"strings"
	"testing"
)

// testDelta 构造只填决策所需字段的 Delta，让测试用例保持简洁。
// ratchet 的决策只读 MeanDiff 与 CILow，其余统计字段不参与判断。
func testDelta(meanDiff, ciLow float64) Delta {
	return Delta{MeanDiff: meanDiff, CILow: ciLow}
}

// hasCJK 报告字符串是否含有 CJK 统一表意文字（即中文字符）。
func hasCJK(s string) bool {
	for _, r := range s {
		if r >= 0x4E00 && r <= 0x9FFF {
			return true
		}
	}
	return false
}

// TestRatchetDecideAcceptReject 用表驱动覆盖接受/拒绝的核心判定，
// 并同时断言 BestScore 与 History 在接受时前进、拒绝时完全不变。
func TestRatchetDecideAcceptReject(t *testing.T) {
	const initial = 0.5
	tests := []struct {
		name       string
		train      Delta
		holdout    Delta
		epsilon    float64
		wantAccept bool
		wantReason string // reason 中必须出现的子串
	}{
		{
			name:       "train improves and holdout stable accepts",
			train:      testDelta(0.05, 0),
			holdout:    testDelta(0, 0.02),
			epsilon:    0.01,
			wantAccept: true,
			wantReason: "接受",
		},
		{
			name:       "holdout significant degradation rejects",
			train:      testDelta(0.05, 0),
			holdout:    testDelta(0, -0.05),
			epsilon:    0.01,
			wantAccept: false,
			wantReason: "保留集显著退化",
		},
		{
			name:       "train zero improvement rejects",
			train:      testDelta(0, 0),
			holdout:    testDelta(0, 0.02),
			epsilon:    0.01,
			wantAccept: false,
			wantReason: "训练集未提升",
		},
		{
			name:       "train regression rejects",
			train:      testDelta(-0.03, 0),
			holdout:    testDelta(0, 0.02),
			epsilon:    0.01,
			wantAccept: false,
			wantReason: "训练集未提升",
		},
		{
			// 严格不等号：CILow 恰等于 -epsilon 视作退化，拒绝。
			name:       "boundary CILow equals negative epsilon rejects",
			train:      testDelta(0.05, 0),
			holdout:    testDelta(0, -0.01),
			epsilon:    0.01,
			wantAccept: false,
			wantReason: "保留集显著退化",
		},
		{
			// 刚好越过边界（CILow 略大于 -epsilon）即接受。
			name:       "boundary CILow just above negative epsilon accepts",
			train:      testDelta(0.05, 0),
			holdout:    testDelta(0, -0.009),
			epsilon:    0.01,
			wantAccept: true,
			wantReason: "接受",
		},
		{
			// 放宽 epsilon 后，原本显著退化的候选也变得可接受。
			name:       "larger epsilon tolerates mild degradation",
			train:      testDelta(0.05, 0),
			holdout:    testDelta(0, -0.02),
			epsilon:    0.05,
			wantAccept: true,
			wantReason: "接受",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := NewRatchetState(initial)
			gotAccept, reason := s.Decide(tc.train, tc.holdout, tc.epsilon)

			if gotAccept != tc.wantAccept {
				t.Fatalf("Decide() accept = %t, want %t (reason=%q)", gotAccept, tc.wantAccept, reason)
			}
			if !strings.Contains(reason, tc.wantReason) {
				t.Fatalf("Decide() reason = %q, want it to contain %q", reason, tc.wantReason)
			}

			wantBest := initial
			wantHistoryLen := 0
			if tc.wantAccept {
				wantBest = initial + tc.train.MeanDiff
				wantHistoryLen = 1
			}
			if s.BestScore != wantBest {
				t.Fatalf("BestScore = %v, want %v", s.BestScore, wantBest)
			}
			if len(s.History) != wantHistoryLen {
				t.Fatalf("len(History) = %d, want %d", len(s.History), wantHistoryLen)
			}
		})
	}
}

// TestRatchetEpsilonDefault 验证 epsilon <= 0 时回退到默认阈值 0.01，
// 区分「用默认值」与「把阈值当成 0」两种行为。
func TestRatchetEpsilonDefault(t *testing.T) {
	tests := []struct {
		name       string
		holdout    Delta
		epsilon    float64
		wantAccept bool
	}{
		{
			// -0.005 未超过默认 0.01 的容忍度：若把 epsilon 当 0 则会误拒绝。
			name:       "zero epsilon uses default and accepts mild noise",
			holdout:    testDelta(0, -0.005),
			epsilon:    0,
			wantAccept: true,
		},
		{
			name:       "negative epsilon uses default and accepts mild noise",
			holdout:    testDelta(0, -0.005),
			epsilon:    -1,
			wantAccept: true,
		},
		{
			name:       "zero epsilon still rejects degradation beyond default",
			holdout:    testDelta(0, -0.02),
			epsilon:    0,
			wantAccept: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := NewRatchetState(0)
			accept, _ := s.Decide(testDelta(0.05, 0), tc.holdout, tc.epsilon)
			if accept != tc.wantAccept {
				t.Fatalf("Decide() accept = %t, want %t", accept, tc.wantAccept)
			}
			if len(s.History) > 0 && s.History[0].Epsilon != defaultRatchetEpsilon {
				t.Fatalf("history Epsilon = %v, want default %v", s.History[0].Epsilon, defaultRatchetEpsilon)
			}
		})
	}
}

// TestRatchetHistoryAccumulation 验证历史只在接受时累积，且逐条记录
// 分数单调前进；被拒绝的候选既不追加历史也不改变 BestScore。
func TestRatchetHistoryAccumulation(t *testing.T) {
	s := NewRatchetState(1.0)
	steps := []struct {
		name       string
		train      Delta
		holdout    Delta
		wantAccept bool
	}{
		{"first accept", testDelta(0.1, 0), testDelta(0, 0.03), true},
		{"rejected does not append", testDelta(0.1, 0), testDelta(0, -0.5), false},
		{"second accept", testDelta(0.2, 0), testDelta(0, 0.01), true},
		{"another rejected", testDelta(0, 0), testDelta(0, 0.03), false},
		{"third accept", testDelta(0.05, 0), testDelta(0, -0.001), true},
	}

	wantBest := 1.0
	for _, st := range steps {
		t.Run(st.name, func(t *testing.T) {
			prevBest := s.BestScore
			accept, _ := s.Decide(st.train, st.holdout, 0.01)
			if accept != st.wantAccept {
				t.Fatalf("Decide() accept = %t, want %t", accept, st.wantAccept)
			}
			if st.wantAccept {
				wantBest = prevBest + st.train.MeanDiff
				last := s.History[len(s.History)-1]
				if last.PrevBestScore != prevBest {
					t.Fatalf("last.PrevBestScore = %v, want %v", last.PrevBestScore, prevBest)
				}
				if last.BestScore != wantBest {
					t.Fatalf("last.BestScore = %v, want %v", last.BestScore, wantBest)
				}
				if !last.Accept {
					t.Fatal("last.Accept = false on an accepted step")
				}
			} else if s.BestScore != prevBest {
				t.Fatalf("rejected step changed BestScore: %v, want %v", s.BestScore, prevBest)
			}
			if s.BestScore != wantBest {
				t.Fatalf("BestScore = %v, want %v", s.BestScore, wantBest)
			}
		})
	}

	// 5 步里恰好 3 次接受，历史长度应为 3；BestScore 严格单调上涨。
	if len(s.History) != 3 {
		t.Fatalf("len(History) = %d, want 3", len(s.History))
	}
	for i := 1; i < len(s.History); i++ {
		if s.History[i].PrevBestScore != s.History[i-1].BestScore {
			t.Fatalf("history gap at %d: PrevBestScore=%v, previous BestScore=%v",
				i, s.History[i].PrevBestScore, s.History[i-1].BestScore)
		}
		if s.History[i].BestScore <= s.History[i-1].BestScore {
			t.Fatalf("BestScore not monotonic at %d: %v <= %v",
				i, s.History[i].BestScore, s.History[i-1].BestScore)
		}
	}
}

// TestRatchetReasonsAreCJK 验证所有决策理由都是中文（CJK），
// 与仓库「中文注释文档」的约定一致。
func TestRatchetReasonsAreCJK(t *testing.T) {
	tests := []struct {
		name    string
		train   Delta
		holdout Delta
	}{
		{"accept", testDelta(0.05, 0), testDelta(0, 0.02)},
		{"reject train", testDelta(0, 0), testDelta(0, 0.02)},
		{"reject holdout", testDelta(0.05, 0), testDelta(0, -0.5)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := NewRatchetState(0)
			_, reason := s.Decide(tc.train, tc.holdout, 0.01)
			if !hasCJK(reason) {
				t.Fatalf("Decide() reason = %q, want it to contain CJK characters", reason)
			}
			if len(s.History) > 0 && !hasCJK(s.History[0].Reason) {
				t.Fatalf("history Reason = %q, want CJK", s.History[0].Reason)
			}
		})
	}
}

// TestRatchetDoesNotMutateDelta 确认 Decide 不修改入参 Delta。
// Delta 按值传递且不含引用成员，此测试锁定这一纯函数性质。
func TestRatchetDoesNotMutateDelta(t *testing.T) {
	train := testDelta(0.05, 0.3)
	holdout := testDelta(0, -0.001)
	trainBefore, holdoutBefore := train, holdout

	s := NewRatchetState(0)
	s.Decide(train, holdout, 0.01)

	if !reflect.DeepEqual(train, trainBefore) {
		t.Fatalf("Decide() mutated train Delta: got %+v, want %+v", train, trainBefore)
	}
	if !reflect.DeepEqual(holdout, holdoutBefore) {
		t.Fatalf("Decide() mutated holdout Delta: got %+v, want %+v", holdout, holdoutBefore)
	}
}

// TestNewRatchetState 验证构造函数设置初始分数且历史为空。
func TestNewRatchetState(t *testing.T) {
	s := NewRatchetState(2.5)
	if s == nil {
		t.Fatal("NewRatchetState() returned nil")
	}
	if s.BestScore != 2.5 {
		t.Fatalf("BestScore = %v, want 2.5", s.BestScore)
	}
	if s.History != nil {
		t.Fatalf("History = %v, want nil", s.History)
	}
}
