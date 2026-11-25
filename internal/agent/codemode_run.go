package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// runCodeMode 执行一段 Code Mode 脚本。每一步都通过 s.callTool 执行，
// 因此策略、审批、审计与超时对每步子调用都完整生效（不是绕过主链的快速路径）。
// 结果聚合为一个工具输出文本；任一步失败立即停止并返回已执行步数与原因。
func (s *runState) runCodeMode(ctx context.Context, raw string) (string, error) {
	steps, err := parseCodeModeScript(raw)
	if err != nil {
		return "", err
	}
	results := make([]stepOutput, 0, len(steps))
	var report strings.Builder
	total := 0
	for i, step := range steps {
		argsJSON := "{}"
		if step.Args != nil {
			interpolated := interpolateArgs(step.Args, results)
			encoded, err := json.Marshal(interpolated)
			if err != nil {
				return report.String(), fmt.Errorf("%w %d: encode args: %v", ErrCodeModeStep, i, err)
			}
			argsJSON = string(encoded)
		}
		out, _, err := s.callTool(ctx, step.Tool, argsJSON)
		if err != nil {
			return report.String(), fmt.Errorf("%w %d (%s): %v", ErrCodeModeStep, i, step.Tool, err)
		}
		if len(out) > codeModeMaxStepOutput {
			out = out[:codeModeMaxStepOutput] + "\n[code mode: step output truncated]"
		}
		results = append(results, stepOutput{Output: out, As: step.As})
		total += len(out)
		report.WriteString(fmt.Sprintf("[%d] %s: %s\n", i, step.Tool, out))
		if total > codeModeMaxOutput {
			return report.String(), fmt.Errorf("%w: aggregate output exceeds %d bytes", ErrCodeMode, codeModeMaxOutput)
		}
	}
	report.WriteString(fmt.Sprintf("code mode: %d step(s) completed\n", len(steps)))
	return report.String(), nil
}
