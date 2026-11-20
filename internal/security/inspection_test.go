package security

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"unicode/utf8"
)

type testInspector struct {
	name    string
	verdict Verdict
	err     error
	calls   atomic.Int64

	mu   sync.Mutex
	args []string
}

func (s *testInspector) Name() string { return s.name }

func (s *testInspector) Inspect(ctx context.Context, tool string, args json.RawMessage) (Verdict, error) {
	s.calls.Add(1)
	s.mu.Lock()
	s.args = append(s.args, string(args))
	s.mu.Unlock()
	return s.verdict, s.err
}

func (s *testInspector) callCount() int64 { return s.calls.Load() }

func (s *testInspector) lastArgs() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.args) == 0 {
		return ""
	}
	return s.args[len(s.args)-1]
}

type panicInspector struct{}

func (*panicInspector) Name() string { return "panicking" }

func (*panicInspector) Inspect(context.Context, string, json.RawMessage) (Verdict, error) {
	panic("inspector exploded")
}

func newTestManager(inspectors ...Inspector) *InspectionManager {
	m := &InspectionManager{}
	m.newFindingID = func(kind string) string { return "SEC-TEST-" + kind }
	for _, inspector := range inspectors {
		m.Add(inspector)
	}
	return m
}

func TestResolveDenyDominatesEveryOrder(t *testing.T) {
	deny := &testInspector{name: "deny", verdict: Verdict{
		Action: InspectDeny, Confidence: 0.4, Reason: "explicit deny", Inspector: "deny", FindingID: "F-DENY",
	}}
	approval := &testInspector{name: "approval", verdict: Verdict{
		Action: InspectRequireApproval, Confidence: 0.99, Reason: "needs a human", Inspector: "approval", FindingID: "F-APPROVAL",
	}}
	allow := &testInspector{name: "allow", verdict: Verdict{
		Action: InspectAllow, Confidence: 1, Reason: "looks safe", Inspector: "allow", FindingID: "F-ALLOW",
	}}

	orders := [][]Inspector{
		{deny, approval, allow},
		{allow, deny, approval},
		{approval, allow, deny},
	}
	for i, order := range orders {
		got := newTestManager(order...).Resolve(context.Background(), "bash", json.RawMessage(`{"command":"ls"}`))
		if got.Action != InspectDeny || got.FindingID != "F-DENY" || got.Reason != "explicit deny" {
			t.Fatalf("order %d: got %+v, want the deny verdict", i, got)
		}
	}
}

func TestResolveApprovalBeatsAllow(t *testing.T) {
	allow := &testInspector{name: "allow", verdict: Verdict{Action: InspectAllow, Confidence: 1, FindingID: "F-ALLOW"}}
	approval := &testInspector{name: "approval", verdict: Verdict{Action: InspectRequireApproval, Confidence: 0.1, FindingID: "F-APPROVAL"}}
	got := newTestManager(allow, approval).Resolve(context.Background(), "bash", nil)
	if got.Action != InspectRequireApproval || got.FindingID != "F-APPROVAL" {
		t.Fatalf("got %+v, want RequireApproval even with lower confidence", got)
	}
}

func TestResolveAllowAlone(t *testing.T) {
	allow := &testInspector{name: "allow", verdict: Verdict{Action: InspectAllow, Confidence: 0.2, FindingID: "F-ALLOW"}}
	got := newTestManager(allow).Resolve(context.Background(), "read", json.RawMessage(`{"path":"a.txt"}`))
	if got.Action != InspectAllow || got.FindingID != "F-ALLOW" {
		t.Fatalf("got %+v, want Allow", got)
	}
}

func TestResolveNoVerdictFailsClosed(t *testing.T) {
	got := (&InspectionManager{}).Resolve(context.Background(), "bash", nil)
	if got.Action != InspectRequireApproval {
		t.Fatalf("got %v, want RequireApproval", got.Action)
	}
	if got.FindingID != "" || got.Reason != "no inspector produced a verdict" {
		t.Fatalf("got %+v, want the exact unknown-request default", got)
	}
}

func TestResolveFailSoftLogsErrorAndContinues(t *testing.T) {
	failing := &testInspector{name: "failing", err: errors.New("boom")}
	good := &testInspector{name: "good", verdict: Verdict{Action: InspectDeny, Confidence: 0.9, Reason: "bad path", FindingID: "F-GOOD"}}
	m := newTestManager(failing, good)

	var mu sync.Mutex
	var logged []string
	m.SetErrorHandler(func(name string, err error) {
		mu.Lock()
		logged = append(logged, name+": "+err.Error())
		mu.Unlock()
	})

	got := m.Resolve(context.Background(), "write", json.RawMessage(`{"path":"../secret"}`))
	if got.Action != InspectDeny || got.FindingID != "F-GOOD" {
		t.Fatalf("got %+v, want the healthy inspector's deny", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(logged) != 1 || logged[0] != "failing: boom" {
		t.Fatalf("logged = %q, want exactly one failing: boom", logged)
	}
	if failing.callCount() != 1 || good.callCount() != 1 {
		t.Fatalf("calls failing=%d good=%d, want one each", failing.callCount(), good.callCount())
	}
}

func TestResolveFailSoftRecoversPanic(t *testing.T) {
	good := &testInspector{name: "good", verdict: Verdict{Action: InspectAllow, FindingID: "F-GOOD"}}
	m := newTestManager(&panicInspector{}, good)

	var mu sync.Mutex
	var panics int
	m.SetErrorHandler(func(name string, err error) {
		mu.Lock()
		if name == "panicking" && strings.Contains(err.Error(), "exploded") {
			panics++
		}
		mu.Unlock()
	})

	got := m.Resolve(context.Background(), "read", nil)
	if got.Action != InspectAllow {
		t.Fatalf("got %+v, want Allow from the surviving inspector", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if panics != 1 {
		t.Fatalf("panic handler called %d times, want 1", panics)
	}
}

func TestResolveAllErrorsFailClosed(t *testing.T) {
	first := &testInspector{name: "first", err: errors.New("one")}
	second := &testInspector{name: "second", err: errors.New("two")}
	m := newTestManager(first, second)
	var reported int
	m.SetErrorHandler(func(string, error) { reported++ })
	got := m.Resolve(context.Background(), "bash", nil)
	if got.Action != InspectRequireApproval || got.Reason != "no inspector produced a verdict" || got.FindingID != "" {
		t.Fatalf("got %+v, want the fail-closed default", got)
	}
	if reported != 2 {
		t.Fatalf("reported %d errors, want both", reported)
	}
}

func TestResolveDisabledInspectorSkipped(t *testing.T) {
	deny := &testInspector{name: "deny", verdict: Verdict{Action: InspectDeny, Confidence: 1, FindingID: "F-DENY"}}
	allow := &testInspector{name: "allow", verdict: Verdict{Action: InspectAllow, FindingID: "F-ALLOW"}}
	m := newTestManager(deny, allow)
	m.Disable("deny")

	got := m.Resolve(context.Background(), "bash", nil)
	if got.Action != InspectAllow || got.FindingID != "F-ALLOW" {
		t.Fatalf("got %+v, want the non-disabled Allow", got)
	}
	if deny.callCount() != 0 || allow.callCount() != 1 {
		t.Fatalf("calls deny=%d allow=%d, want 0 and 1", deny.callCount(), allow.callCount())
	}
}

func TestResolveAllDisabledFailsClosed(t *testing.T) {
	m := newTestManager(&testInspector{name: "only", verdict: Verdict{Action: InspectAllow}})
	m.Disable("only")
	got := m.Resolve(context.Background(), "bash", nil)
	if got.Action != InspectRequireApproval || got.Reason != "no inspector produced a verdict" {
		t.Fatalf("got %+v, want the fail-closed default", got)
	}
}

func TestResolveHighestConfidenceWinsWithFirstWinsTie(t *testing.T) {
	low := &testInspector{name: "low", verdict: Verdict{Action: InspectDeny, Confidence: 0.2, FindingID: "F-LOW"}}
	high := &testInspector{name: "high", verdict: Verdict{Action: InspectDeny, Confidence: 0.95, FindingID: "F-HIGH"}}
	got := newTestManager(low, high).Resolve(context.Background(), "bash", nil)
	if got.FindingID != "F-HIGH" {
		t.Fatalf("got %s, want the higher-confidence deny", got.FindingID)
	}

	tieFirst := &testInspector{name: "tie-first", verdict: Verdict{Action: InspectDeny, Confidence: 0.7, FindingID: "F-FIRST"}}
	tieSecond := &testInspector{name: "tie-second", verdict: Verdict{Action: InspectDeny, Confidence: 0.7, FindingID: "F-SECOND"}}
	got = newTestManager(tieFirst, tieSecond).Resolve(context.Background(), "bash", nil)
	if got.FindingID != "F-FIRST" {
		t.Fatalf("got %s, want the first inspector on a confidence tie", got.FindingID)
	}
}

func TestResolveUnknownActionFailsClosed(t *testing.T) {
	broken := &testInspector{name: "broken", verdict: Verdict{Action: InspectionAction(99), FindingID: "F-BROKEN"}}
	got := newTestManager(broken).Resolve(context.Background(), "bash", nil)
	if got.Action != InspectRequireApproval {
		t.Fatalf("got %v, want unknown actions downgraded to RequireApproval", got.Action)
	}
}

func TestResolveGeneratesFindingIDWhenInspectorOmitsIt(t *testing.T) {
	noID := &testInspector{name: "no-id", verdict: Verdict{Action: InspectDeny, Reason: "blocked"}}
	got := newTestManager(noID).Resolve(context.Background(), "bash", nil)
	if got.FindingID != "SEC-TEST-DENY" {
		t.Fatalf("FindingID = %q, want the injected generator's value", got.FindingID)
	}
}

func TestResolveBoundsAndRedactsVerdictFields(t *testing.T) {
	long := strings.Repeat("x", 2000)
	secret := "sk-proj-abcdefghijklmnop"
	ins := &testInspector{name: "leaky", verdict: Verdict{
		Action:     InspectDeny,
		Confidence: 0.9,
		Reason:     "leak " + secret + " " + long,
		Inspector:  "leaky-" + long,
		FindingID:  "F-" + long,
	}}
	got := newTestManager(ins).Resolve(context.Background(), "bash", nil)

	if got.Action != InspectDeny {
		t.Fatalf("got %v, want Deny", got.Action)
	}
	if strings.Contains(got.Reason, secret) {
		t.Fatal("verdict reason leaked a credential")
	}
	if !strings.Contains(got.Reason, "[redacted:api key]") {
		t.Fatalf("reason was not redacted: %q", got.Reason)
	}
	if utf8.RuneCountInString(got.Reason) > maxVerdictReasonRunes ||
		utf8.RuneCountInString(got.Inspector) > maxVerdictNameRunes ||
		utf8.RuneCountInString(got.FindingID) > maxVerdictFindingRunes {
		t.Fatalf("unbounded verdict fields: %+v", got)
	}
}

func TestResolveBoundsVerdictOnRuneBoundary(t *testing.T) {
	ins := &testInspector{name: "unicode", verdict: Verdict{
		Action: InspectDeny,
		Reason: strings.Repeat("界", maxVerdictReasonRunes+10),
	}}
	got := newTestManager(ins).Resolve(context.Background(), "bash", nil)
	if !utf8.ValidString(got.Reason) {
		t.Fatal("truncated reason is not valid UTF-8")
	}
	if utf8.RuneCountInString(got.Reason) != maxVerdictReasonRunes {
		t.Fatalf("reason has %d runes, want %d", utf8.RuneCountInString(got.Reason), maxVerdictReasonRunes)
	}
}

func TestNewFindingIDFormatAndUniqueness(t *testing.T) {
	id := NewFindingID("deny")
	if !strings.HasPrefix(id, "SEC-") || !strings.HasSuffix(id, "-DENY") {
		t.Fatalf("id %q has the wrong format", id)
	}
	seen := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		id := NewFindingID("")
		if len(id) != len("SEC-")+16 {
			t.Fatalf("id %q has unexpected length %d", id, len(id))
		}
		if seen[id] {
			t.Fatalf("duplicate finding ID %q", id)
		}
		seen[id] = true
	}
	if got := len(findingKind(strings.Repeat("z", 100))); got > maxFindingKindRunes {
		t.Fatalf("finding kind length %d exceeds %d", got, maxFindingKindRunes)
	}
}

func TestResolveCapsArgumentSize(t *testing.T) {
	ins := &testInspector{name: "ok", verdict: Verdict{Action: InspectAllow, FindingID: "F-ALLOW"}}
	m := newTestManager(ins)

	huge := json.RawMessage(`{"blob":"` + strings.Repeat("a", maxInspectionArgs) + `"}`)
	if len(huge) <= maxInspectionArgs {
		t.Fatal("test fixture is not oversized")
	}
	got := m.Resolve(context.Background(), "bash", huge)
	if got.Action != InspectRequireApproval || got.FindingID == "" {
		t.Fatalf("oversized args: got %+v, want fail-closed RequireApproval with a finding ID", got)
	}
	if ins.callCount() != 0 {
		t.Fatalf("inspector was called %d times with oversized args, want 0", ins.callCount())
	}

	atCap := json.RawMessage(strings.Repeat("a", maxInspectionArgs))
	got = m.Resolve(context.Background(), "bash", atCap)
	if got.Action != InspectAllow || ins.callCount() != 1 || len(ins.lastArgs()) != maxInspectionArgs {
		t.Fatalf("args at the cap: got %+v, calls=%d", got, ins.callCount())
	}
}

func TestResolveConcurrentWithReconfiguration(t *testing.T) {
	m := &InspectionManager{}
	m.newFindingID = func(kind string) string { return "SEC-TEST" }
	stable := &testInspector{name: "stable", verdict: Verdict{Action: InspectAllow, FindingID: "F-STABLE"}}
	m.Add(stable)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				got := m.Resolve(context.Background(), "bash", json.RawMessage(`{"command":"ls"}`))
				if got.Action != InspectAllow || got.FindingID != "F-STABLE" {
					t.Errorf("worker %d iteration %d: got %+v", id, j, got)
					return
				}
				name := fmt.Sprintf("extra-%d-%d", id, j)
				if id%3 == 0 {
					m.Add(&testInspector{name: name, verdict: Verdict{Action: InspectAllow, FindingID: "F-EXTRA"}})
				}
				if id%4 == 0 {
					m.Disable(name)
				}
				if id%5 == 0 {
					m.SetErrorHandler(func(string, error) {})
				}
			}
		}(i)
	}
	wg.Wait()
}
