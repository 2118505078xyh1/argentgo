package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// commandRecorder 线程安全地记录 fake runner 与 middleware 写入的事件。
// runTier 会并发调用 runner，因此记录必须加锁。
type commandRecorder struct {
	mu       sync.Mutex
	commands []string
}

func (r *commandRecorder) add(command string) {
	r.mu.Lock()
	r.commands = append(r.commands, command)
	r.mu.Unlock()
}

func (r *commandRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.commands...)
}

// middlewareTestDispatcher 构造只使用 fake runner 的 Dispatcher：不启动任何
// shell 进程，runner 把命令名记入 recorder 并返回成功的 Outcome。
func middlewareTestDispatcher(rec *commandRecorder) *Dispatcher {
	return &Dispatcher{
		Runner: func(_ context.Context, command Command, _ Payload) Outcome {
			rec.add(command.Command)
			return Outcome{Event: command.Event, Command: command.Command, ExitCode: 0}
		},
	}
}

// TestDispatchWithoutMiddlewaresMatchesBase 验证零中间件的分发与旧的
// Run+Summarize 路径在 Result 三字段、命令序列与次数上完全一致。
func TestDispatchWithoutMiddlewaresMatchesBase(t *testing.T) {
	// 各命令优先级不同，runTier 按层级顺序执行，使记录顺序确定。
	config := &Config{Commands: []Command{
		{Event: PreToolUse, Command: "low", Priority: 1, Timeout: time.Second},
		{Event: PreToolUse, Command: "mid", Priority: 5, Timeout: time.Second},
		{Event: PreToolUse, Command: "high", Priority: 10, Timeout: time.Second},
	}}
	payload := Payload{Event: "PreToolUse", Tool: "bash", Args: json.RawMessage(`{"command":"ls"}`)}

	recDispatch := &commandRecorder{}
	got, err := middlewareTestDispatcher(recDispatch).Dispatch(context.Background(), config, payload)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	recBase := &commandRecorder{}
	outcomes, err := middlewareTestDispatcher(recBase).Run(context.Background(), config, payload)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	want := Summarize(outcomes)

	if got.Blocked != want.Blocked || got.Message != want.Message || got.Continuation != want.Continuation {
		t.Fatalf("Dispatch = %+v, want Run+Summarize = %+v", got, want)
	}
	if gotSeq, wantSeq := strings.Join(recDispatch.snapshot(), ","), strings.Join(recBase.snapshot(), ","); gotSeq != wantSeq {
		t.Fatalf("command sequence = %q, want %q", gotSeq, wantSeq)
	}
}

// TestMiddlewareOrderIsOnion 验证三个中间件按洋葱模型包裹 base。
func TestMiddlewareOrderIsOnion(t *testing.T) {
	rec := &commandRecorder{}
	dispatcher := middlewareTestDispatcher(rec)
	middleware := func(name string) Middleware {
		return func(_ context.Context, _ Event, _ *Payload, next func() (Result, error)) (Result, error) {
			rec.add(name + "-before")
			result, err := next()
			rec.add(name + "-after")
			return result, err
		}
	}
	dispatcher.Middlewares = []Middleware{middleware("m1"), middleware("m2"), middleware("m3")}
	config := &Config{Commands: []Command{{Event: PreToolUse, Command: "base", Timeout: time.Second}}}

	if _, err := dispatcher.Dispatch(context.Background(), config, Payload{Event: "PreToolUse"}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	want := []string{"m1-before", "m2-before", "m3-before", "base", "m3-after", "m2-after", "m1-after"}
	got := rec.snapshot()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("onion order = %v, want %v", got, want)
	}
}

// TestMiddlewareShortCircuit 验证 middleware 不调用 next 时 base 完全不执行。
func TestMiddlewareShortCircuit(t *testing.T) {
	var calls atomic.Int32
	dispatcher := &Dispatcher{
		Runner: func(_ context.Context, command Command, _ Payload) Outcome {
			calls.Add(1)
			return Outcome{Event: command.Event, Command: command.Command, ExitCode: 0}
		},
		Middlewares: []Middleware{
			func(_ context.Context, _ Event, _ *Payload, _ func() (Result, error)) (Result, error) {
				return Result{Blocked: true, Message: "blocked by middleware"}, nil
			},
		},
	}
	config := &Config{Commands: []Command{{Event: PreToolUse, Command: "should-not-run", Timeout: time.Second}}}

	result, err := dispatcher.Dispatch(context.Background(), config, Payload{Event: "PreToolUse"})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !result.Blocked || result.Message != "blocked by middleware" {
		t.Fatalf("short-circuit result = %+v", result)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("base runner called %d times, want 0", got)
	}
}

// TestMiddlewareArgsMutation 验证 middleware 改写 args 后 base 收到改写值。
func TestMiddlewareArgsMutation(t *testing.T) {
	var seen json.RawMessage
	dispatcher := &Dispatcher{
		Runner: func(_ context.Context, command Command, payload Payload) Outcome {
			seen = append(json.RawMessage(nil), payload.Args...)
			return Outcome{Event: command.Event, Command: command.Command, ExitCode: 0}
		},
		Middlewares: []Middleware{
			func(_ context.Context, _ Event, args *Payload, next func() (Result, error)) (Result, error) {
				args.Args = json.RawMessage(`{"rewritten":true}`)
				return next()
			},
		},
	}
	config := &Config{Commands: []Command{{Event: PreToolUse, Command: "base", Timeout: time.Second}}}

	if _, err := dispatcher.Dispatch(context.Background(), config, Payload{
		Event: "PreToolUse", Args: json.RawMessage(`{"original":true}`),
	}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if string(seen) != `{"rewritten":true}` {
		t.Fatalf("base saw args %s, want the rewritten value", seen)
	}
}

// TestMiddlewarePanic 验证 middleware panic 被 guard 转成 ErrCallbackPanic，
// 且 panic 之后的中间件不再执行。
func TestMiddlewarePanic(t *testing.T) {
	var ran atomic.Int32
	counting := func(_ context.Context, _ Event, _ *Payload, next func() (Result, error)) (Result, error) {
		ran.Add(1)
		return next()
	}
	dispatcher := middlewareTestDispatcher(&commandRecorder{})
	dispatcher.Middlewares = []Middleware{
		func(_ context.Context, _ Event, _ *Payload, _ func() (Result, error)) (Result, error) {
			panic("middleware boom")
		},
		counting,
		counting,
	}
	config := &Config{Commands: []Command{{Event: PreToolUse, Command: "base", Timeout: time.Second}}}

	_, err := dispatcher.Dispatch(context.Background(), config, Payload{Event: "PreToolUse"})
	if !errors.Is(err, ErrCallbackPanic) {
		t.Fatalf("panic error = %v, want ErrCallbackPanic", err)
	}
	if got := ran.Load(); got != 0 {
		t.Fatalf("%d later middlewares ran after panic, want 0", got)
	}
}

// TestMiddlewareConcurrentIsolation 验证共享同一个 Dispatcher 并发分发时，
// 每次调用的 middleware 状态（args 指针）互不串扰。
func TestMiddlewareConcurrentIsolation(t *testing.T) {
	t.Parallel()
	const n = 32
	observed := make(chan int, n)
	dispatcher := &Dispatcher{
		Runner: func(_ context.Context, command Command, payload Payload) Outcome {
			var got map[string]int
			if err := json.Unmarshal(payload.Args, &got); err != nil {
				return Outcome{Event: command.Event, Command: command.Command, ExitCode: 1}
			}
			observed <- got["id"]
			return Outcome{Event: command.Event, Command: command.Command, ExitCode: 0}
		},
		Middlewares: []Middleware{
			func(_ context.Context, _ Event, args *Payload, next func() (Result, error)) (Result, error) {
				var in map[string]int
				if err := json.Unmarshal(args.Args, &in); err != nil {
					return Result{}, err
				}
				id := in["id"]
				// 主动让出调度，放大共享状态错误时的串扰窗口。
				runtime.Gosched()
				rewritten, err := json.Marshal(map[string]int{"id": id})
				if err != nil {
					return Result{}, err
				}
				args.Args = rewritten
				return next()
			},
		},
	}
	config := &Config{Commands: []Command{{Event: PreToolUse, Command: "base", Timeout: time.Second}}}

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			payload := Payload{Event: "PreToolUse", Args: json.RawMessage(fmt.Sprintf(`{"id":%d}`, id))}
			if _, err := dispatcher.Dispatch(context.Background(), config, payload); err != nil {
				t.Errorf("dispatch %d: %v", id, err)
			}
		}(i)
	}
	wg.Wait()
	close(observed)

	seen := make(map[int]int)
	for id := range observed {
		seen[id]++
	}
	for id := 0; id < n; id++ {
		if seen[id] != 1 {
			t.Fatalf("id %d observed %d times, want exactly once (middleware state leaked across dispatches)", id, seen[id])
		}
	}
}
