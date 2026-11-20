package security

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

// fakeApprover 记录每次 Ask 调用并返回可配置的回答。它可并发安全使用，因此能为
// -race 并发测试提供支撑。
type fakeApprover struct {
	mu      sync.Mutex
	answer  bool
	err     error
	calls   int
	records []approvalRequest
}

type approvalRequest struct {
	name string
	args string
}

func (f *fakeApprover) Ask(ctx context.Context, name string, args string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.records = append(f.records, approvalRequest{name: name, args: args})
	return f.answer, f.err
}

func (f *fakeApprover) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeApprover) recorded() []approvalRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]approvalRequest, len(f.records))
	copy(out, f.records)
	return out
}

func TestScopedApproverDelegatesWithoutGrant(t *testing.T) {
	inner := &fakeApprover{answer: true}
	scoped := NewScopedApprover(inner)
	ok, err := scoped.Ask(context.Background(), "bash", `{"command":"ls"}`)
	if err != nil || !ok {
		t.Fatalf("got ok=%v err=%v, want the inner approve", ok, err)
	}
	if got := inner.callCount(); got != 1 {
		t.Fatalf("inner asked %d times, want once", got)
	}
}

func TestScopedApproverGrantAlwaysShortCircuits(t *testing.T) {
	inner := &fakeApprover{answer: false}
	scoped := NewScopedApprover(inner)
	scoped.GrantAlways("bash")
	for i := 0; i < 3; i++ {
		ok, err := scoped.Ask(context.Background(), "bash", "{}")
		if err != nil || !ok {
			t.Fatalf("call %d: ok=%v err=%v, want an unconditional approve", i, ok, err)
		}
	}
	if got := inner.callCount(); got != 0 {
		t.Fatalf("an always grant consulted inner %d times, want never", got)
	}
}

func TestScopedApproverGrantNConsumesThenDelegates(t *testing.T) {
	inner := &fakeApprover{answer: false}
	scoped := NewScopedApprover(inner)
	scoped.GrantN("bash", 2)
	for i := 0; i < 2; i++ {
		ok, err := scoped.Ask(context.Background(), "bash", "{}")
		if err != nil || !ok {
			t.Fatalf("granted call %d: ok=%v err=%v", i, ok, err)
		}
	}
	if got := inner.callCount(); got != 0 {
		t.Fatalf("granted calls consulted inner %d times, want never", got)
	}
	ok, err := scoped.Ask(context.Background(), "bash", "{}")
	if err != nil || ok {
		t.Fatalf("third call should delegate to the denying inner: ok=%v err=%v", ok, err)
	}
	if got := inner.callCount(); got != 1 {
		t.Fatalf("third call consulted inner %d times, want once", got)
	}
}

func TestScopedApproverAlwaysWinsAndDoesNotConsumeN(t *testing.T) {
	inner := &fakeApprover{answer: false}
	scoped := NewScopedApprover(inner)
	scoped.GrantN("bash", 2)
	scoped.GrantAlways("bash")
	for i := 0; i < 4; i++ {
		ok, err := scoped.Ask(context.Background(), "bash", "{}")
		if err != nil || !ok {
			t.Fatalf("call %d: ok=%v err=%v", i, ok, err)
		}
	}
	if got := inner.callCount(); got != 0 {
		t.Fatalf("always grant consulted inner %d times, want never", got)
	}
	if got := scoped.limited["bash"]; got != 2 {
		t.Fatalf("always grant consumed the N budget: %d remaining, want 2", got)
	}
}

func TestScopedApproverPassesArgsThroughOnDelegation(t *testing.T) {
	inner := &fakeApprover{answer: true}
	scoped := NewScopedApprover(inner)
	ctx := context.Background()
	const name = "write"
	const args = "{\"path\":\"a.txt\",\"body\":\"héllo\\n世界\"}"
	if _, err := scoped.Ask(ctx, name, args); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	records := inner.recorded()
	if len(records) != 1 {
		t.Fatalf("inner recorded %d calls, want one", len(records))
	}
	if records[0].name != name || records[0].args != args {
		t.Fatalf("inner got name=%q args=%q, want name=%q args=%q", records[0].name, records[0].args, name, args)
	}
}

func TestScopedApproverConcurrentAskIsSafe(t *testing.T) {
	inner := &fakeApprover{answer: true}
	scoped := NewScopedApprover(inner)
	scoped.GrantAlways("always")
	scoped.GrantN("limited", 10)

	const workers = 50
	var wg sync.WaitGroup
	errs := make(chan string, workers*2)

	for i := 0; i < workers; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			ok, err := scoped.Ask(context.Background(), "always", "{}")
			if err != nil || !ok {
				errs <- fmt.Sprintf("always: ok=%v err=%v", ok, err)
			}
		}()
		go func() {
			defer wg.Done()
			ok, err := scoped.Ask(context.Background(), "limited", "{}")
			if err != nil || !ok {
				errs <- fmt.Sprintf("limited: ok=%v err=%v", ok, err)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Error(msg)
	}
	// 只有超出 10 次授予预算的那 40 次调用才可能到达 inner；always 调用绝不能到达。
	if got := inner.callCount(); got != workers-10 {
		t.Fatalf("inner asked %d times, want %d", got, workers-10)
	}
}

func TestScopedApproverConcurrentGrantsAreSafe(t *testing.T) {
	inner := &fakeApprover{answer: false}
	scoped := NewScopedApprover(inner)

	const workers = 50
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("tool-%d", i)
			scoped.GrantAlways(name)
			scoped.GrantN("shared", 1)
		}(i)
	}
	wg.Wait()

	for i := 0; i < workers; i++ {
		name := fmt.Sprintf("tool-%d", i)
		if ok, err := scoped.Ask(context.Background(), name, "{}"); err != nil || !ok {
			t.Fatalf("%s: ok=%v err=%v", name, ok, err)
		}
	}
	for i := 0; i < workers; i++ {
		if ok, err := scoped.Ask(context.Background(), "shared", "{}"); err != nil || !ok {
			t.Fatalf("shared grant %d: ok=%v err=%v", i, ok, err)
		}
	}
	if got := inner.callCount(); got != 0 {
		t.Fatalf("inner asked %d times, want never", got)
	}
}

// TestScopedApproverDenyOverridesAlways 是核心的 deny 优先保证：一个曾被永久授予的
// 名字一旦被拒绝就会被拒绝，无论两个决定以何种顺序记录。这里内部审批器批准一切，
// 因此 allow 结果将证明 deny 没有短路。
func TestScopedApproverDenyOverridesAlways(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*ScopedApprover)
	}{
		{"deny after always", func(s *ScopedApprover) { s.GrantAlways("bash"); s.Deny("bash") }},
		{"deny before always", func(s *ScopedApprover) { s.Deny("bash"); s.GrantAlways("bash") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inner := &fakeApprover{answer: true}
			scoped := NewScopedApprover(inner)
			tc.setup(scoped)
			for i := 0; i < 3; i++ {
				ok, err := scoped.Ask(context.Background(), "bash", "{}")
				if err != nil || ok {
					t.Fatalf("call %d: ok=%v err=%v, want an unconditional refusal", i, ok, err)
				}
			}
			if got := inner.callCount(); got != 0 {
				t.Fatalf("denied name consulted inner %d times, want never", got)
			}
		})
	}
}

// TestScopedApproverDenyShortCircuitsNGrantAndDelegation 检查 deny 在有限授予之前
// （也会在 inner 之前）被查询：N 预算保持不动，且不发生委托。
func TestScopedApproverDenyShortCircuitsNGrantAndDelegation(t *testing.T) {
	inner := &fakeApprover{answer: true}
	scoped := NewScopedApprover(inner)
	scoped.GrantN("bash", 1)
	scoped.Deny("bash")
	for i := 0; i < 2; i++ {
		if ok, err := scoped.Ask(context.Background(), "bash", "{}"); err != nil || ok {
			t.Fatalf("call %d: ok=%v err=%v, want refusal", i, ok, err)
		}
	}
	if got := scoped.limited["bash"]; got != 1 {
		t.Fatalf("deny consumed the N budget: %d remaining, want 1", got)
	}
	if got := inner.callCount(); got != 0 {
		t.Fatalf("denied name consulted inner %d times, want never", got)
	}
}

// TestScopedApproverRevokePrefixClearsAllBuckets 验证扩展卸载路径：撤销 "srv__" 会
// 丢弃该服务器所有工具的永久授予、N 次授予和拒绝，而另一服务器的授予仍然存在。
// 内部审批器拒绝一切，因此任何不再有本地决定的名字都会落到它那里。
func TestScopedApproverRevokePrefixClearsAllBuckets(t *testing.T) {
	inner := &fakeApprover{answer: false}
	scoped := NewScopedApprover(inner)
	scoped.GrantAlways("srv__read")
	scoped.GrantN("srv__write", 2)
	scoped.Deny("srv__admin")
	scoped.GrantAlways("other__read")

	if got := scoped.RevokePrefix("srv__"); got != 3 {
		t.Fatalf("RevokePrefix removed %d entries, want 3", got)
	}

	for _, name := range []string{"srv__read", "srv__write", "srv__admin"} {
		ok, err := scoped.Ask(context.Background(), name, "{}")
		if err != nil || ok {
			t.Fatalf("%s after revoke: ok=%v err=%v, want delegation to denying inner", name, ok, err)
		}
	}
	if got := inner.callCount(); got != 3 {
		t.Fatalf("inner asked %d times after revoke, want 3 (one per revoked name)", got)
	}

	// 不同前缀下的授予不受影响，仍然短路。
	if ok, err := scoped.Ask(context.Background(), "other__read", "{}"); err != nil || !ok {
		t.Fatalf("other__read: ok=%v err=%v, want the surviving always grant", ok, err)
	}
	if got := inner.callCount(); got != 3 {
		t.Fatalf("surviving grant consulted inner, call count now %d, want 3", got)
	}
}

// TestScopedApproverDenyOnOneNameDoesNotAffectAnother 防止 deny 桶泄漏到无关工具：
// 被拒绝的名字不得阻止另一个名字的正常永久授予。
func TestScopedApproverDenyOnOneNameDoesNotAffectAnother(t *testing.T) {
	inner := &fakeApprover{answer: false}
	scoped := NewScopedApprover(inner)
	scoped.Deny("dangerous")
	scoped.GrantAlways("safe")

	for i := 0; i < 2; i++ {
		if ok, err := scoped.Ask(context.Background(), "safe", "{}"); err != nil || !ok {
			t.Fatalf("safe call %d: ok=%v err=%v, want an unconditional approve", i, ok, err)
		}
	}
	if ok, err := scoped.Ask(context.Background(), "dangerous", "{}"); err != nil || ok {
		t.Fatalf("dangerous: ok=%v err=%v, want refusal", ok, err)
	}
	if got := inner.callCount(); got != 0 {
		t.Fatalf("inner asked %d times, want never", got)
	}
}
