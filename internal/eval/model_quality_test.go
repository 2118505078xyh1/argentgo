package eval

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}

func TestLoadAndRunModelQualityFile(t *testing.T) {
	path := filepath.Join("..", "..", "evals", "model-quality.json")
	doc, err := LoadModelQualityFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(doc.Tasks) != 3 {
		t.Fatalf("tasks = %d, want 3", len(doc.Tasks))
	}
	report := RunModelQuality(doc)
	if report.Tasks != 3 {
		t.Fatalf("report.Tasks = %d", report.Tasks)
	}
	// 三个任务中前两个完成，第三个因 max_turns=2 未完成 → 2/3。
	if report.Completed != 2 {
		t.Fatalf("completed = %d, want 2", report.Completed)
	}
	if report.CompletionRate < 0.66 || report.CompletionRate > 0.67 {
		t.Fatalf("completion rate = %f, want ~0.667", report.CompletionRate)
	}
	if report.SampleCount != 3 {
		t.Fatalf("sample count = %d", report.SampleCount)
	}
	// significant=false 且样本量小 → 质量报告必须给出不完整原因与告警。
	if report.Quality.IncompleteReason == "" || len(report.Quality.Warnings) == 0 {
		t.Fatalf("expected quality warnings: %+v", report.Quality)
	}
}

func TestLoadModelQualityFileRejectsInvalid(t *testing.T) {
	cases := []struct {
		name string
		json string
	}{
		{"no tasks", `{"name":"x","tasks":[]}`},
		{"no steps", `{"name":"x","tasks":[{"name":"a","steps":[]}]}`},
		{"length mismatch", `{"name":"x","tasks":[{"name":"a","steps":["read"],"paths":["a","b"]}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "q.json")
			if err := writeFile(path, tc.json); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadModelQualityFile(path); err == nil {
				t.Fatal("expected load error")
			}
		})
	}
}
