package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/eval"
)

// evalFixtureRun 是一次 fixture 运行的完整结果。runErr 与 setupErr 分开：
// 完成率类 fixture 含负对照，eval.Run 按设计返回非 nil runErr，但报告仍然完整；
// setupErr 才表示加载/计数失败，测试应直接失败。
type evalFixtureRun struct {
	report   eval.Report
	runErr   error
	setupErr error
	tap      string
}

// runEvalFixturePath 在不依赖 *testing.T 的情况下跑一个 eval fixture，
// 使结果能被包级缓存复用（testing.T 不能跨测试保存）。
func runEvalFixturePath(path string, wantCount int) evalFixtureRun {
	scenarios, err := eval.Load(path)
	if err != nil {
		return evalFixtureRun{setupErr: err}
	}
	if wantCount > 0 && len(scenarios) != wantCount {
		return evalFixtureRun{setupErr: fmt.Errorf("scenario count = %d, want %d", len(scenarios), wantCount)}
	}
	runner := NewEvaler(cli.IO{
		Out: io.Discard,
		Err: io.Discard,
		Env: func(string) string { return "" },
	})
	var out bytes.Buffer
	report, runErr := eval.Run(
		context.Background(),
		scenarios,
		runner.(*evaler).runScenario,
		eval.Options{Out: &out},
	)
	return evalFixtureRun{report: report, runErr: runErr, tap: out.String()}
}

var (
	completionBenchOnce   sync.Once
	completionBenchCached evalFixtureRun
)

// cachedCompletionBenchRun 返回 completion_bench.json 的基线运行结果。
// 这个 6 场景套件会被完成率测试与敏感性自检各自跑一遍；每次约 7-9 秒，
// 且结果确定（脚本化 provider、断网、无 API key），因此缓存一次供两者共享。
func cachedCompletionBenchRun() evalFixtureRun {
	completionBenchOnce.Do(func() {
		_, thisFile, _, ok := runtime.Caller(0)
		if !ok {
			completionBenchCached = evalFixtureRun{setupErr: fmt.Errorf("cannot locate test file")}
			return
		}
		path := filepath.Join(filepath.Dir(thisFile), "..", "..", "evals", "completion_bench.json")
		completionBenchCached = runEvalFixturePath(path, 6)
	})
	return completionBenchCached
}
