package app

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/hooks"
	"github.com/2118505078xyh1/argentgo/internal/llm"
)

func TestResolveMaxParallel(t *testing.T) {
	cases := []struct {
		name   string
		config string
		flag   int
		want   int
	}{
		{name: "default when unset", want: defaultMaxParallel},
		{name: "camel case config", config: `{"maxParallel":7}`, want: 7},
		{name: "kebab case config", config: `{"max-parallel":6}`, want: 6},
		{name: "flag beats config", config: `{"maxParallel":7}`, flag: 3, want: 3},
		{name: "one keeps the batch serial", config: `{"maxParallel":1}`, want: 1},
		{name: "zero config is unset", config: `{"maxParallel":0}`, want: defaultMaxParallel},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.config != "" {
				writeFile(t, filepath.Join(root, "argent.json"), tc.config)
			}
			env, _ := testEnv(t)
			res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: root, MaxParallel: tc.flag})
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if res.MaxParallel != tc.want {
				t.Fatalf("MaxParallel = %d, want %d", res.MaxParallel, tc.want)
			}
		})
	}
}

// concurrentDispatcher records the peak number of overlapping PreToolUse
// dispatches. A serial batch cannot produce a peak above one.
type concurrentDispatcher struct {
	mu       sync.Mutex
	inflight int
	peak     int
	delay    time.Duration
}

func (d *concurrentDispatcher) Dispatch(ctx context.Context, _ *hooks.Config, payload hooks.Payload) (hooks.Result, error) {
	if payload.Event != string(hooks.PreToolUse) {
		return hooks.Result{}, nil
	}
	d.mu.Lock()
	d.inflight++
	if d.inflight > d.peak {
		d.peak = d.inflight
	}
	d.mu.Unlock()
	select {
	case <-time.After(d.delay):
	case <-ctx.Done():
	}
	d.mu.Lock()
	d.inflight--
	d.mu.Unlock()
	return hooks.Result{}, nil
}

func (d *concurrentDispatcher) peakCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.peak
}

// TestExecuteRunsReadOnlyBatchConcurrently proves the production wiring: the
// Resolved value from Resolve carries MaxParallel into agent.Options, so two
// read-only calls in one batch overlap instead of running one after another.
func TestExecuteRunsReadOnlyBatchConcurrently(t *testing.T) {
	env, _ := testEnv(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "alpha-from-a")
	writeFile(t, filepath.Join(root, "b.txt"), "bravo-from-b")
	writeFile(t, filepath.Join(root, ".argent", "hooks.json"), hookDocument)

	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: root})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.MaxParallel <= 1 {
		t.Fatalf("resolved MaxParallel = %d, want > 1", res.MaxParallel)
	}
	trustHookFile(t, filepath.Join(root, ".argent", "hooks.json"), res.Storage)

	dispatcher := &concurrentDispatcher{delay: 60 * time.Millisecond}
	res.HookDispatcher = dispatcher
	res.Approve = autoApprover{}
	res.Chatter = &toolCallChatter{first: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{
		{ID: "c1", Type: "function", Function: llm.ToolCallFunction{Name: "read", Arguments: `{"path":"a.txt"}`}},
		{ID: "c2", Type: "function", Function: llm.ToolCallFunction{Name: "read", Arguments: `{"path":"b.txt"}`}},
	}}}

	store := testStore(t, res)
	sess, err := store.NewSession("parallel-reads")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(context.Background(), cli.IO{Env: env}, res, sess, "read both", nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if peak := dispatcher.peakCount(); peak < 2 {
		t.Fatalf("peak concurrent PreToolUse = %d, want >= 2 (the batch ran serially)", peak)
	}
	if !toolOutputContains(t, sess, "alpha-from-a") || !toolOutputContains(t, sess, "bravo-from-b") {
		t.Fatal("a read result is missing from the transcript")
	}
}
