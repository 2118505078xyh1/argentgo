package agent

import "testing"

// TestShouldStopTurnPriorityAndReasons 覆盖各停止原因、优先级、边界与组合。
// 每行都描述"哪些信号同时为真时，应当由谁胜出并给出什么原因"。
func TestShouldStopTurnPriorityAndReasons(t *testing.T) {
	tests := []struct {
		name     string
		turns    int
		maxTurns int
		budget   bool
		canceled bool
		reason   string
		wantStop bool
		wantWhy  string
	}{
		// 空：没有任何信号触发。
		{"no signals", 0, 10, false, false, "", false, ""},
		{"empty reason below limit", 0, 1, false, false, "", false, ""},

		// 取消：最高优先级，覆盖其余一切信号。
		{"cancel alone", 0, 10, false, true, "", true, StopCancelled},
		{"cancel beats budget", 2, 10, true, true, StopMaxCost, true, StopCancelled},
		{"cancel beats max turns", 5, 5, false, true, "", true, StopCancelled},
		{"cancel beats existing reason", 1, 10, false, true, StopComplete, true, StopCancelled},
		{"cancel beats everything", 9, 5, true, true, StopProviderError, true, StopCancelled},

		// 预算：透传调用方给出的具体原因，且优先于轮次上限。
		{"budget max cost reason", 1, 10, true, false, StopMaxCost, true, StopMaxCost},
		{"budget max total reason", 1, 10, true, false, StopMaxTotalTokens, true, StopMaxTotalTokens},
		{"budget max prompt reason", 1, 10, true, false, StopMaxPromptTokens, true, StopMaxPromptTokens},
		{"budget declared reason", 1, 10, true, false, StopDeclaredTurnBudget, true, StopDeclaredTurnBudget},
		{"budget beats max turns", 5, 5, true, false, StopMaxCost, true, StopMaxCost},
		{"budget without reason falls back", 1, 10, true, false, "", true, StopMaxTotalTokens},
		{"budget without reason beats max turns", 5, 5, true, false, "", true, StopMaxTotalTokens},

		// 轮次上限：边界与优先于既有原因。
		{"turns at limit", 5, 5, false, false, "", true, StopMaxTurns},
		{"turns one below limit", 4, 5, false, false, "", false, ""},
		{"turns over limit", 7, 5, false, false, "", true, StopMaxTurns},
		{"max turns beats existing reason", 5, 5, false, false, StopComplete, true, StopMaxTurns},
		{"max turns beats cjk reason", 5, 5, false, false, "预算超限", true, StopMaxTurns},

		// 既有原因：没有更高优先级信号时原样透传。
		{"existing complete reason", 1, 10, false, false, StopComplete, true, StopComplete},
		{"existing provider error reason", 1, 10, false, false, StopProviderError, true, StopProviderError},
		{"existing output limit reason", 1, 10, false, false, StopOutputLimit, true, StopOutputLimit},
		{"existing verify reason", 1, 10, false, false, StopVerifyReflections, true, StopVerifyReflections},
		{"existing repeated mistakes reason", 1, 10, false, false, stopRepeatedMistakes, true, stopRepeatedMistakes},
		{"existing stuck reason", 1, 10, false, false, StopStuck, true, StopStuck},

		// 边界：非正 maxTurns 表示不限轮次；负数 turns 不触发虚警。
		{"zero max turns means unlimited", 100, 0, false, false, "", false, ""},
		{"negative max turns means unlimited", 100, -1, false, false, "", false, ""},
		{"zero max turns keeps existing reason", 100, 0, false, false, StopComplete, true, StopComplete},
		{"negative turns below limit", -1, 5, false, false, "", false, ""},
		{"negative turns at negative limit", -1, -1, false, false, "", false, ""},

		// CJK 无关：非 ASCII 原因按字节原样透传，不做内容解析。
		{"cjk existing reason passthrough", 1, 10, false, false, "预算超限，请结束", true, "预算超限，请结束"},
		{"cjk budget reason passthrough", 1, 10, true, false, "预算耗尽", true, "预算耗尽"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stop, why := shouldStopTurn(tt.turns, tt.maxTurns, tt.budget, tt.canceled, tt.reason)
			if stop != tt.wantStop || why != tt.wantWhy {
				t.Fatalf(
					"shouldStopTurn(%d, %d, %v, %v, %q) = (%v, %q), want (%v, %q)",
					tt.turns, tt.maxTurns, tt.budget, tt.canceled, tt.reason,
					stop, why, tt.wantStop, tt.wantWhy,
				)
			}
		})
	}
}

// TestShouldStopTurnStoppingReasonNeverEmpty 保证"判定停止 => 原因非空"这一不变式，
// 尤其是预算突破但调用方漏传原因的退化情形也必须落到兜底原因上。
func TestShouldStopTurnStoppingReasonNeverEmpty(t *testing.T) {
	cases := []struct {
		name     string
		turns    int
		maxTurns int
		budget   bool
		canceled bool
		reason   string
	}{
		{"budget empty reason", 1, 10, true, false, ""},
		{"budget empty reason at limit", 5, 5, true, false, ""},
		{"max turns empty reason", 5, 5, false, false, ""},
		{"cancel empty reason", 1, 10, false, true, ""},
		{"all empty reason", 1, 10, true, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stop, why := shouldStopTurn(tc.turns, tc.maxTurns, tc.budget, tc.canceled, tc.reason)
			if !stop {
				t.Fatal("expected a stop decision")
			}
			if why == "" {
				t.Fatal("a stop decision must always carry a non-empty reason")
			}
		})
	}
}

// TestShouldStopTurnIsStable 验证决策不受调用顺序或外部状态影响：
// 穿插调用其它参数后，相同输入仍返回完全相同的结果。
func TestShouldStopTurnIsStable(t *testing.T) {
	wantStop, wantWhy := shouldStopTurn(3, 5, true, false, StopMaxCost)
	for i := 0; i < 50; i++ {
		// 穿插一组截然不同的信号，检验函数没有隐藏的可变状态。
		shouldStopTurn(99, 0, false, true, "预算超限")
		shouldStopTurn(0, 1, false, false, "")
		stop, why := shouldStopTurn(3, 5, true, false, StopMaxCost)
		if stop != wantStop || why != wantWhy {
			t.Fatalf("call %d: got (%v, %q), want (%v, %q)", i, stop, why, wantStop, wantWhy)
		}
	}
}
