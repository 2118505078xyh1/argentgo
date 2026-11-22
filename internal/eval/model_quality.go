package eval

import (
	"encoding/json"
	"fmt"
	"os"
)

// 本文件把 long_task 的纯原语、completion rate 与 measurement quality 汇总
// 串成一条无网络的离线评测路径：数据文件提供脚本化的长任务（工具调用 transcript），
// EvaluateTask 逐条核对步骤顺序，TaskCompletionRate 汇总完成率，再用
// AssessMeasurementQuality 把样本量与显著性折算成可解释的质量报告。

// ModelQualityTask 是 model-quality.json 中的一个脚本化长任务。
// Steps 为期望步骤（按顺序命中），Transcript 为一次运行的工具调用序列。
type ModelQualityTask struct {
	Name       string   `json:"name"`
	Steps      []string `json:"steps"`      // 每一步期望命中的工具名子串；空串表示该步只校验路径
	Paths      []string `json:"paths"`      // 与 Steps 对齐的路径子串约束；可缺省
	MaxTurns   int      `json:"max_turns"`  // <=0 表示不限
	Transcript []string `json:"transcript"` // 脚本化工具调用序列
}

// ModelQualityFile 是 model-quality.json 的顶层结构。
type ModelQualityFile struct {
	Name  string             `json:"name"`
	Tasks []ModelQualityTask `json:"tasks"`
	// Significant 由数据声明：这批样本的差异是否显著（人工/外部统计结论）。
	Significant bool `json:"significant"`
}

// ModelQualityReport 是评估结果。字段都是纯数据，便于 CLI 渲染与测试断言。
type ModelQualityReport struct {
	Name           string
	Tasks          int
	Completed      int
	CompletionRate float64
	SampleCount    int
	Quality        MeasurementQuality
}

// LoadModelQualityFile 从 path 读取并校验 model-quality.json。
func LoadModelQualityFile(path string) (ModelQualityFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ModelQualityFile{}, fmt.Errorf("eval: read model-quality file %s: %w", path, err)
	}
	var doc ModelQualityFile
	if err := json.Unmarshal(data, &doc); err != nil {
		return ModelQualityFile{}, fmt.Errorf("eval: parse model-quality file %s: %w", path, err)
	}
	if len(doc.Tasks) == 0 {
		return ModelQualityFile{}, fmt.Errorf("eval: model-quality file %s has no tasks", path)
	}
	for i, task := range doc.Tasks {
		if len(task.Steps) == 0 {
			return ModelQualityFile{}, fmt.Errorf("eval: model-quality task %d (%q) has no steps", i, task.Name)
		}
		if len(task.Paths) > 0 && len(task.Paths) != len(task.Steps) {
			return ModelQualityFile{}, fmt.Errorf("eval: model-quality task %d (%q) steps/paths length mismatch", i, task.Name)
		}
	}
	return doc, nil
}

// RunModelQuality 对数据文件中的每个脚本化长任务运行 EvaluateTask，
// 汇总完成率与测量质量。它是纯函数：不修改 doc，不触碰网络或模型。
func RunModelQuality(doc ModelQualityFile) ModelQualityReport {
	results := make([]TaskResult, 0, len(doc.Tasks))
	for _, task := range doc.Tasks {
		longTask := LongTask{
			Name:     task.Name,
			MaxTurns: task.MaxTurns,
			Steps:    make([]TaskStep, 0, len(task.Steps)),
		}
		for i, step := range task.Steps {
			path := ""
			if i < len(task.Paths) {
				path = task.Paths[i]
			}
			longTask.Steps = append(longTask.Steps, TaskStep{ExpectedTool: step, ExpectedPath: path})
		}
		results = append(results, EvaluateTask(longTask, task.Transcript))
	}
	completed := 0
	for _, result := range results {
		if result.Completed {
			completed++
		}
	}
	name := doc.Name
	if name == "" {
		name = "model-quality"
	}
	return ModelQualityReport{
		Name:           name,
		Tasks:          len(doc.Tasks),
		Completed:      completed,
		CompletionRate: TaskCompletionRate(results),
		SampleCount:    len(results),
		Quality:        AssessMeasurementQuality(len(results), doc.Significant),
	}
}
