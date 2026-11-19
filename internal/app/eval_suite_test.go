package app

import (
	"bytes"
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/2118505078xyh1/argent-go/internal/cli"
)

func TestEvalSuiteRunsCoreScenariosThroughTheRealLoop(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test file")
	}
	path := filepath.Join(filepath.Dir(thisFile), "..", "..", "evals", "core.json")
	var out bytes.Buffer
	runner := NewEvaler(cli.IO{Out: &out, Err: &out, Env: func(string) string { return "" }})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	passed, failed, err := runner.(*evaler).RunSuite(ctx, path, &out)
	if err != nil || failed != 0 {
		t.Fatalf("RunSuite = %d passed, %d failed, %v\n%s", passed, failed, err, out.String())
	}
	if passed != 10 {
		t.Fatalf("passed = %d, want 10\n%s", passed, out.String())
	}
	for _, want := range []string{"1..10", "ok 1 - answers a simple question", "ok 8 - the turn limit ends a run"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("report misses %q:\n%s", want, out.String())
		}
	}
}
