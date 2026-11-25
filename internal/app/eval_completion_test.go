package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"os/exec"
	"testing"

	"example.com/argentgo/internal/cli"
	"example.com/argentgo/internal/eval"
)

// R23 完成率验收闭环的端到端证明：用两个 Go 内建 eval.Scenario（不依赖 JSON/golden/网络），
// 经由 app.evaler.runScenario 驱动真实 agent 循环，证明 grader 能区分"已修复"和"未修复"。
// 判定不靠模型自评（收尾文本说什么都行），而靠 go test 的退出码与 FileEquals 的逐字节比对。
//
// 双场景共用同一份种子工作区：
//   - F（fix）：脚本先 read calc.go，再 edit 把减法改成加法，收尾文本；go test 退出 0 → Pass。
//   - N（noop）：脚本只 read calc.go 然后收尾文本（不 edit）；go test 仍退出 1 → Fail。
//
// 两者 Expect 不同脚本，但 Check 完全一致（同一个 Exec + 同一个 FileEquals），
// 于是"完成/未完成"的差别只来自 agent 是否真的把文件改对了，而非提示词自评。
const (
	completionGoMod = "module example.com/calcfix\n\ngo 1.21\n"

	// 种子实现有 bug：Add 做的是减法。
	completionCalcSeed = "package calc\n\nfunc Add(a, b int) int { return a - b }\n"
	// 修复后期望的逐字节内容（含结尾换行），供 FileEquals 精确比对。
	completionCalcFixed = "package calc\n\nfunc Add(a, b int) int { return a + b }\n"

	// 测试断言 Add(1,2)==3：减法时为 -1 → 失败，加法时为 3 → 通过。
	completionCalcTest = `package calc

import "testing"

func TestAdd(t *testing.T) {
	if got := Add(1, 2); got != 3 {
		t.Fatalf("Add(1, 2) = %d, want 3", got)
	}
}
`
)

// completionIntPtr 返回指向 v 的指针，用于填充 eval.Expect 的指针字段。
func completionIntPtr(v int) *int { return &v }

// completionSeedFiles 返回两个场景共用的种子文件（每次调用返回新 map，避免场景间共享可变状态）。
func completionSeedFiles() map[string]string {
	return map[string]string{
		"go.mod":       completionGoMod,
		"calc.go":      completionCalcSeed,
		"calc_test.go": completionCalcTest,
	}
}

// completionCheck 返回两个场景共用的 Check：同一个 Exec（go test ./... WantExitCode 0）
// 加上同一个 FileEquals（calc.go 必须逐字节等于修复后的内容）。
func completionCheck() eval.Check {
	return eval.Check{
		AllowExec: true,
		Exec: &eval.ExecCheck{
			Command:      "go",
			Args:         []string{"test", "./..."},
			TimeoutMS:    30000,
			WantExitCode: 0,
		},
		FileEquals: map[string]string{
			"calc.go": completionCalcFixed,
		},
	}
}

// completionFixScenario 构造场景 F：脚本读文件、修 bug、收尾；期望通过。
func completionFixScenario() eval.Scenario {
	return eval.Scenario{
		Name:   "fix the subtraction bug (should pass)",
		Prompt: "read calc.go, fix Add so the test passes, then report what you did",
		Files:  completionSeedFiles(),
		Provider: []eval.ScriptTurn{
			// 第一轮：读 calc.go。
			{
				Respond: eval.Respond{
					ToolCalls: []eval.ToolCall{
						{Name: "read", Arguments: json.RawMessage(`{"path":"calc.go"}`)},
					},
				},
			},
			// 第二轮：请求里必须已经回注了读到的文件内容（needle 回注证明），
			// 然后 edit 把减法改成加法。
			{
				ExpectRequestContains: []string{"return a - b"},
				Respond: eval.Respond{
					ToolCalls: []eval.ToolCall{
						{
							Name: "edit",
							Arguments: json.RawMessage(
								`{"path":"calc.go","old":"return a - b","new":"return a + b"}`),
						},
					},
				},
			},
			// 第三轮：收尾文本。它说什么都不影响评分，评分只看退出码和文件字节。
			{
				Respond: eval.Respond{Text: "I changed Add to use addition; the test should pass now."},
			},
		},
		Expect: eval.Expect{
			ToolCalls: []string{"read", "edit"},
			Turns:     completionIntPtr(3),
		},
		Check: completionCheck(),
	}
}

// completionNoopScenario 构造场景 N：脚本只读文件、不修 bug；期望失败。
// Check 与 F 完全相同，因此失败只能来自文件没被改对（go test 退出 1 + 字节不匹配）。
func completionNoopScenario() eval.Scenario {
	return eval.Scenario{
		Name:   "read only, no fix (should fail)",
		Prompt: "read calc.go and report what it contains",
		Files:  completionSeedFiles(),
		Provider: []eval.ScriptTurn{
			{
				Respond: eval.Respond{
					ToolCalls: []eval.ToolCall{
						{Name: "read", Arguments: json.RawMessage(`{"path":"calc.go"}`)},
					},
				},
			},
			// 回注检查同样成立：读到的内容确实回到上下文；但模型不做任何修改。
			{
				ExpectRequestContains: []string{"return a - b"},
				Respond:               eval.Respond{Text: "The file currently subtracts b from a."},
			},
		},
		Expect: eval.Expect{
			ToolCalls: []string{"read"},
			Turns:     completionIntPtr(2),
		},
		Check: completionCheck(),
	}
}

// TestEvalCompletionRateFixPassesNoopFails 是 R23 完成率闭环的端到端证明。
//
// 它把 F/N 两个内建场景交给 eval.Run，RunFunc 用 app 的真实 evaler.runScenario
// （真实 agent 循环 + 真实工具注册表 + 真实安全链，脚本化 provider，断网且无 API key）。
// 断言：1 通过 1 失败；ScenarioCompletionRate 复用长任务完成率语义，恰好为 0.5。
// 这证明 grader 依据的是 go test 退出码 + 文件字节，而不是模型的收尾自评。
func TestEvalCompletionRateFixPassesNoopFails(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}

	runner := NewEvaler(cli.IO{
		Out: io.Discard,
		Err: io.Discard,
		Env: func(string) string { return "" },
	})
	var out bytes.Buffer
	report, err := eval.Run(
		context.Background(),
		[]eval.Scenario{completionFixScenario(), completionNoopScenario()},
		runner.(*evaler).runScenario,
		eval.Options{Out: &out},
	)
	// N 场景应当失败，所以 eval.Run 返回非 nil 错误；报告仍然完整。
	if err == nil {
		t.Fatalf("eval.Run error = nil, want a failure for the noop scenario; report:\n%+v\n%s", report, out.String())
	}
	if report.Passed != 1 || report.Failed != 1 {
		t.Fatalf("report = %d passed, %d failed; want 1 and 1\n%+v\n%s",
			report.Passed, report.Failed, report, out.String())
	}
	// 完成率必须复用长任务语义：1/2 = 0.5。
	if got := eval.ScenarioCompletionRate(report.Results); math.Abs(got-0.5) > 1e-9 {
		t.Fatalf("ScenarioCompletionRate = %v, want 0.5; report:\n%+v\n%s", got, report, out.String())
	}
}
