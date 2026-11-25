package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// runCodeMode 执行一段 Code Mode 脚本。每一步都通过 s.callTool 执行，
// 因此策略、审批、审计与超时对每步子调用都完整生效（不是绕过主链的快速路径）。
//
// 两种模式：
//   - 默认顺序：与旧 DSL 逐字节一致，任一步失败立即停止。
//   - parallel:true：按 dependsOn（缺省为全部前序步骤）分波执行，同一波内的步骤
//     并发调用，波与波之间严格同步。这样无依赖的只读步骤不再串行等待。
//
// return 字段限定进入最终报告的步骤：大输出可以做中间计算并插值给后续工具，
// 只有被选中的小结果回到模型上下文，从根上避免"大输出二次进上下文"。
func (s *runState) runCodeMode(ctx context.Context, raw string) (string, error) {
	program, err := parseCodeModeProgram(raw)
	if err != nil {
		return "", err
	}
	steps := program.Steps
	results := make([]stepOutput, len(steps))
	completed := 0
	total := 0

	if program.Parallel {
		completed, total, err = s.runCodeModeParallel(ctx, steps, results)
	} else {
		for i, step := range steps {
			out, stepErr := execCodeModeStep(ctx, s, step, results)
			if stepErr != nil {
				return renderCodeModeReport(program, steps, results), stepErr
			}
			results[i] = stepOutput{Output: out, As: step.As, Done: true}
			completed++
			total += len(out)
			if total > codeModeMaxOutput {
				return renderCodeModeReport(program, steps, results),
					fmt.Errorf("%w: aggregate output exceeds %d bytes", ErrCodeMode, codeModeMaxOutput)
			}
		}
	}
	if err != nil {
		return renderCodeModeReport(program, steps, results), err
	}

	report := renderCodeModeReport(program, steps, results)
	return report + fmt.Sprintf("code mode: %d step(s) completed\n", completed), nil
}

// execCodeModeStep 执行一步：先把前序输出插值进参数，再走完整工具链，最后限幅。
func execCodeModeStep(ctx context.Context, s *runState, step CodeModeStep, results []stepOutput) (string, error) {
	argsJSON := "{}"
	if step.Args != nil {
		encoded, err := json.Marshal(interpolateArgs(step.Args, results))
		if err != nil {
			return "", fmt.Errorf("%w: encode args: %v", ErrCodeModeStep, err)
		}
		argsJSON = string(encoded)
	}
	out, _, err := s.callTool(ctx, step.Tool, argsJSON)
	if err != nil {
		return "", fmt.Errorf("%w (%s): %v", ErrCodeModeStep, step.Tool, err)
	}
	return truncateCodeModeStep(out), nil
}

func truncateCodeModeStep(out string) string {
	if len(out) > codeModeMaxStepOutput {
		return out[:codeModeMaxStepOutput] + "\n[code mode: step output truncated]"
	}
	return out
}

// codeModeDeps 计算每步依赖的下标。parallel 是模型显式选择，因此未声明
// dependsOn 的步骤视为相互独立、同一波并发；声明后只依赖指定步骤（as 名或
// 序号）。顺序模式不走这里，仍严格按脚本顺序执行。
func codeModeDeps(steps []CodeModeStep) [][]int {
	deps := make([][]int, len(steps))
	for i, step := range steps {
		for _, ref := range step.DependsOn {
			if idx := resolveCodeModeRef(ref, steps); idx >= 0 {
				deps[i] = append(deps[i], idx)
			}
		}
	}
	return deps
}

// resolveCodeModeRef 把 as 名或十进制序号解析为步骤下标。
func resolveCodeModeRef(ref string, steps []CodeModeStep) int {
	ref = strings.TrimSpace(ref)
	if n, err := strconv.Atoi(ref); err == nil {
		return n
	}
	for i, step := range steps {
		if step.As == ref {
			return i
		}
	}
	return -1
}

// runCodeModeParallel 按依赖分波并发执行。同一波内在锁下合并结果，wave 之间用
// WaitGroup 严格同步，因此波内只读取已完成步骤的输出，不存在插值竞态。
func (s *runState) runCodeModeParallel(ctx context.Context, steps []CodeModeStep, results []stepOutput) (int, int, error) {
	deps := codeModeDeps(steps)
	done := make([]bool, len(steps))
	completed, total := 0, 0
	var firstErr error
	for remaining := len(steps); remaining > 0; {
		wave := make([]int, 0)
		for i := range steps {
			if done[i] {
				continue
			}
			ready := true
			for _, dep := range deps[i] {
				if !done[dep] {
					ready = false
					break
				}
			}
			if ready {
				wave = append(wave, i)
			}
		}
		if len(wave) == 0 {
			// dependsOn 成环或指向不存在的步骤：fail-closed，绝不静默少跑。
			return completed, total, fmt.Errorf("%w: dependency cycle or unresolved dependency", ErrCodeMode)
		}
		var wg sync.WaitGroup
		var mu sync.Mutex
		for _, i := range wave {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				out, err := execCodeModeStep(ctx, s, steps[i], results)
				mu.Lock()
				defer mu.Unlock()
				done[i] = true
				if err != nil {
					if firstErr == nil {
						firstErr = err
					}
					return
				}
				results[i] = stepOutput{Output: out, As: steps[i].As, Done: true}
				completed++
				total += len(out)
			}(i)
		}
		wg.Wait()
		if firstErr != nil {
			return completed, total, firstErr
		}
		if total > codeModeMaxOutput {
			return completed, total, fmt.Errorf("%w: aggregate output exceeds %d bytes", ErrCodeMode, codeModeMaxOutput)
		}
		remaining -= len(wave)
	}
	return completed, total, nil
}

// renderCodeModeReport 按原始顺序拼接被选中的步骤输出。return 为空时全部返回，
// 保持旧脚本逐字节一致；非空时只回传被选中的步骤，让大输出留在中间层。
func renderCodeModeReport(program CodeModeProgram, steps []CodeModeStep, results []stepOutput) string {
	var report strings.Builder
	selected := make(map[int]bool, len(steps))
	if len(program.Return) == 0 {
		for i := range steps {
			selected[i] = true
		}
	} else {
		for _, ref := range program.Return {
			if idx := resolveCodeModeRef(ref, steps); idx >= 0 && idx < len(steps) {
				selected[idx] = true
			}
		}
	}
	for i, result := range results {
		if !selected[i] || !result.Done {
			continue
		}
		report.WriteString(fmt.Sprintf("[%d] %s: %s\n", i, steps[i].Tool, result.Output))
	}
	return report.String()
}
