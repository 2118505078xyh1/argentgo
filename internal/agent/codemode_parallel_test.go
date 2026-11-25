package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"example.com/argentgo/internal/llm"
)

// recordingTools 记录每次调用与并发峰值，并让调用短暂重叠以观察并行度。
type recordingTools struct {
	mu      sync.Mutex
	calls   []string
	active  int
	maxSeen int
	delay   time.Duration
}

func (r *recordingTools) Specs() []llm.ToolSpec { return nil }
func (r *recordingTools) Run(_ context.Context, name, args string) (string, error) {
	r.mu.Lock()
	r.calls = append(r.calls, name+" "+args)
	r.active++
	if r.active > r.maxSeen {
		r.maxSeen = r.active
	}
	r.mu.Unlock()
	if r.delay > 0 {
		time.Sleep(r.delay)
	}
	r.mu.Lock()
	r.active--
	r.mu.Unlock()
	return "out-" + name, nil
}

func TestParseCodeModeProgramExtras(t *testing.T) {
	program, err := parseCodeModeProgram(`{"parallel":true,"return":["clean"],"steps":[{"tool":"read","as":"raw"},{"tool":"grep","as":"clean","dependsOn":["raw"]}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if !program.Parallel || len(program.Return) != 1 || program.Return[0] != "clean" {
		t.Fatalf("program extras = %+v", program)
	}
	if program.Steps[1].DependsOn[0] != "raw" {
		t.Fatalf("dependsOn lost: %+v", program.Steps[1])
	}
}

func TestCodeModeParallelRunsUnrelatedStepsConcurrently(t *testing.T) {
	tools := &recordingTools{delay: 20 * time.Millisecond}
	s := &runState{tools: tools}
	report, err := s.runCodeMode(context.Background(),
		`{"parallel":true,"steps":[{"tool":"read"},{"tool":"grep"},{"tool":"glob"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if tools.maxSeen != 3 {
		t.Fatalf("max concurrency = %d, want 3", tools.maxSeen)
	}
	if !strings.Contains(report, "[0] read") || !strings.Contains(report, "[2] glob") {
		t.Fatalf("report lost ordered steps: %s", report)
	}
}

func TestCodeModeParallelDependencyInterpolation(t *testing.T) {
	tools := &recordingTools{}
	s := &runState{tools: tools}
	_, err := s.runCodeMode(context.Background(),
		`{"parallel":true,"steps":[{"tool":"read","as":"big"},{"tool":"grep","as":"small","dependsOn":["big"],"args":{"q":"{{vars.big}}"}}]}`)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, call := range tools.calls {
		if strings.Contains(call, "out-read") {
			found = true
		}
	}
	if !found {
		t.Fatalf("dependency output not interpolated into follow-up: %+v", tools.calls)
	}
}

func TestCodeModeReturnFiltersLargeIntermediateOutput(t *testing.T) {
	tools := &recordingTools{}
	s := &runState{tools: tools}
	report, err := s.runCodeMode(context.Background(),
		`{"steps":[{"tool":"read"},{"tool":"grep","as":"clean"},{"tool":"glob"}],"return":["clean"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(report, "[1] grep") || strings.Contains(report, "[0] read") || strings.Contains(report, "[2] glob") {
		t.Fatalf("return filter failed: %s", report)
	}
}

func TestCodeModeParallelDependencyCycleFailsClosed(t *testing.T) {
	s := &runState{tools: &recordingTools{}}
	_, err := s.runCodeMode(context.Background(),
		`{"parallel":true,"steps":[{"tool":"read","as":"a","dependsOn":["b"]},{"tool":"grep","as":"b","dependsOn":["a"]}]}`)
	if !errors.Is(err, ErrCodeMode) {
		t.Fatalf("cycle error = %v", err)
	}
}
