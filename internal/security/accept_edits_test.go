package security

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

// modeTestRequest 记录一次委托给 base 的调用，供参数透传断言使用。
type modeTestRequest struct {
	name string
	args string
}

// modeTestApprover 是可并发安全使用的替身，用于观察 WrapForMode 是否真正
// 委托了调用，以及委托了几次。
type modeTestApprover struct {
	mu      sync.Mutex
	answer  bool
	err     error
	calls   int
	records []modeTestRequest
}

func (a *modeTestApprover) Ask(_ context.Context, name, args string) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	a.records = append(a.records, modeTestRequest{name: name, args: args})
	return a.answer, a.err
}

func (a *modeTestApprover) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

func (a *modeTestApprover) recorded() []modeTestRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]modeTestRequest, len(a.records))
	copy(out, a.records)
	return out
}

func TestParseApprovalModeCanonical(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want ApprovalMode
	}{
		{"ask", ModeAsk},
		{"never", ModeNever},
		{"auto", ModeAuto},
		{"yolo", ModeYolo},
		{"accept-edits", ModeAcceptEdits},
	} {
		if got := ParseApprovalMode(tc.in); got != tc.want {
			t.Errorf("ParseApprovalMode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseApprovalModeCaseInsensitiveAndAliases(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want ApprovalMode
	}{
		{"AcCePt-EdItS", ModeAcceptEdits},
		{"  ACCEPT_EDITS  ", ModeAcceptEdits},
		{"Accept Edits", ModeAcceptEdits},
		{"acceptEdits", ModeAcceptEdits},
		{" NEVER ", ModeNever},
		{"YoLo", ModeYolo},
	} {
		if got := ParseApprovalMode(tc.in); got != tc.want {
			t.Errorf("ParseApprovalMode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// ParseApprovalMode 的入参是 string，无法为 nil；空串是等价的安全入口。
func TestParseApprovalModeUnknownFallsBackToAsk(t *testing.T) {
	for _, in := range []string{"", "   ", "bogus", "on-request", "accept_all"} {
		if got := ParseApprovalMode(in); got != ModeAsk {
			t.Errorf("ParseApprovalMode(%q) = %q, want %q", in, got, ModeAsk)
		}
	}
}

func TestWrapForModePassesThroughNonAcceptEdits(t *testing.T) {
	base := &modeTestApprover{answer: true}
	for _, mode := range []ApprovalMode{ModeAsk, ModeNever, ModeAuto, ModeYolo, ApprovalMode("unknown")} {
		if got := WrapForMode(base, mode); got != base {
			t.Errorf("mode %q: WrapForMode returned a different approver", mode)
		}
		if got := base.count(); got != 0 {
			t.Fatalf("mode %q: WrapForMode called the base approver %d times", mode, got)
		}
	}
}

func TestWrapForModeDoesNotMutateBase(t *testing.T) {
	base := &modeTestApprover{answer: true}
	wrapped := WrapForMode(base, ModeAcceptEdits)
	if _, ok := wrapped.(*ScopedApprover); !ok {
		t.Fatalf("accept-edits must wrap with ScopedApprover, got %T", wrapped)
	}
	if !base.answer || base.err != nil || base.count() != 0 {
		t.Fatal("WrapForMode must not mutate or call the base approver")
	}
}

func TestWrapForModeAcceptEditsGrantsAfterFirstApproval(t *testing.T) {
	base := &modeTestApprover{answer: true}
	wrapped := WrapForMode(base, ModeAcceptEdits)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if ok, err := wrapped.Ask(ctx, "bash", `{"command":"ls"}`); err != nil || !ok {
			t.Fatalf("call %d: ok=%v err=%v, want an approve", i, ok, err)
		}
	}
	if got := base.count(); got != 1 {
		t.Fatalf("same tool asked the operator %d times, want once", got)
	}
	// 作用域按工具名划分：另一个工具仍需询问。
	if ok, err := wrapped.Ask(ctx, "write", `{}`); err != nil || !ok {
		t.Fatalf("write: ok=%v err=%v", ok, err)
	}
	if got := base.count(); got != 2 {
		t.Fatalf("a different tool must prompt again, total %d calls", got)
	}
}

func TestWrapForModeAcceptEditsDoesNotCacheRefusal(t *testing.T) {
	base := &modeTestApprover{answer: false}
	wrapped := WrapForMode(base, ModeAcceptEdits)
	for i := 0; i < 3; i++ {
		if ok, err := wrapped.Ask(context.Background(), "bash", "{}"); err != nil || ok {
			t.Fatalf("call %d: ok=%v err=%v, want a refusal", i, ok, err)
		}
	}
	if got := base.count(); got != 3 {
		t.Fatalf("a refusal must not be cached: base asked %d times, want 3", got)
	}
}

func TestWrapForModeAcceptEditsDoesNotCacheError(t *testing.T) {
	sentinel := errors.New("approval channel down")
	base := &modeTestApprover{err: sentinel}
	wrapped := WrapForMode(base, ModeAcceptEdits)
	for i := 0; i < 2; i++ {
		ok, err := wrapped.Ask(context.Background(), "bash", "{}")
		if ok || !errors.Is(err, sentinel) {
			t.Fatalf("call %d: ok=%v err=%v, want the base error", i, ok, err)
		}
	}
	if got := base.count(); got != 2 {
		t.Fatalf("an error must not be cached: base asked %d times, want 2", got)
	}
}

func TestWrapForModeAcceptEditsConcurrentAskIsSafe(t *testing.T) {
	base := &modeTestApprover{answer: true}
	wrapped := WrapForMode(base, ModeAcceptEdits)
	ctx := context.Background()
	if ok, err := wrapped.Ask(ctx, "bash", "{}"); err != nil || !ok {
		t.Fatalf("warm-up: ok=%v err=%v", ok, err)
	}

	const workers = 50
	var wg sync.WaitGroup
	errs := make(chan string, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, err := wrapped.Ask(ctx, "bash", "{}"); err != nil || !ok {
				errs <- fmt.Sprintf("cached call: ok=%v err=%v", ok, err)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Error(msg)
	}
	if got := base.count(); got != 1 {
		t.Fatalf("a cached name hit the base %d times, want 1 (warm-up only)", got)
	}
}

func TestWrapForModeAcceptEditsConcurrentDistinctCJKNames(t *testing.T) {
	base := &modeTestApprover{answer: true}
	wrapped := WrapForMode(base, ModeAcceptEdits)
	ctx := context.Background()

	const workers = 32
	var wg sync.WaitGroup
	errs := make(chan string, workers*2)
	nameOf := func(i int) string { return fmt.Sprintf("工具-%d", i) }
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if ok, err := wrapped.Ask(ctx, nameOf(i), `{"path":"中文.txt"}`); err != nil || !ok {
				errs <- fmt.Sprintf("%s first call: ok=%v err=%v", nameOf(i), ok, err)
			}
		}(i)
	}
	wg.Wait()
	if got := base.count(); got != workers {
		t.Fatalf("distinct CJK names asked the base %d times, want %d", got, workers)
	}

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if ok, err := wrapped.Ask(ctx, nameOf(i), "{}"); err != nil || !ok {
				errs <- fmt.Sprintf("%s cached call: ok=%v err=%v", nameOf(i), ok, err)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Error(msg)
	}
	if got := base.count(); got != workers {
		t.Fatalf("cached CJK names hit the base %d more times, want 0", got-workers)
	}
}

func TestWrapForModeAcceptEditsCJKToolName(t *testing.T) {
	base := &modeTestApprover{answer: true}
	wrapped := WrapForMode(base, ModeAcceptEdits)
	const name = "编辑文件"
	const args = `{"path":"目录/世界.txt","内容":"你好"}`
	for i := 0; i < 2; i++ {
		if ok, err := wrapped.Ask(context.Background(), name, args); err != nil || !ok {
			t.Fatalf("call %d: ok=%v err=%v", i, ok, err)
		}
	}
	records := base.recorded()
	if len(records) != 1 {
		t.Fatalf("base recorded %d calls, want 1", len(records))
	}
	if records[0].name != name || records[0].args != args {
		t.Fatalf("base got name=%q args=%q, want name=%q args=%q", records[0].name, records[0].args, name, args)
	}
}

func TestWrapForModeAcceptEditsNilBaseDenies(t *testing.T) {
	wrapped := WrapForMode(nil, ModeAcceptEdits)
	if wrapped == nil {
		t.Fatal("a nil base must still yield a usable approver")
	}
	if ok, err := wrapped.Ask(context.Background(), "bash", "{}"); err != nil || ok {
		t.Fatalf("nil base: ok=%v err=%v, want a plain deny", ok, err)
	}
}
