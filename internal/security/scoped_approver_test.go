package security

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

// fakeApprover records every Ask call and returns a configurable answer. It is
// safe for concurrent use so it can back the -race concurrency test.
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
	// Only the 40 calls past the 10-grant budget may reach inner; always calls
	// must never reach it.
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

// TestScopedApproverDenyOverridesAlways is the core deny-wins guarantee: a name
// that was always-granted is still refused once it has been denied, regardless
// of the order the two decisions were recorded. The inner approver approves
// everything here, so an allow result would prove the deny did not short-circuit.
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

// TestScopedApproverDenyShortCircuitsNGrantAndDelegation checks that deny is
// consulted before the bounded grant (and would be before inner): the N budget
// is left untouched and no delegation happens.
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

// TestScopedApproverRevokePrefixClearsAllBuckets verifies the extension-uninstall
// path: revoking "srv__" drops the always grant, the N grant and the refusal for
// every tool from that server, while a different server's grant survives. The
// inner approver denies, so any name that no longer has a local decision falls
// through to it.
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

	// A grant under a different prefix is untouched and still short-circuits.
	if ok, err := scoped.Ask(context.Background(), "other__read", "{}"); err != nil || !ok {
		t.Fatalf("other__read: ok=%v err=%v, want the surviving always grant", ok, err)
	}
	if got := inner.callCount(); got != 3 {
		t.Fatalf("surviving grant consulted inner, call count now %d, want 3", got)
	}
}

// TestScopedApproverDenyOnOneNameDoesNotAffectAnother guards against a deny
// bucket leaking to unrelated tools: a denied name must not block a normal
// always grant for a different name.
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
