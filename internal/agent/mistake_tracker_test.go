package agent

import (
	"fmt"
	"sync"
	"testing"
)

// TestMistakeKindValues pins the wire/string values of the kinds so logs and
// injected guidance can match on a stable vocabulary.
func TestMistakeKindValues(t *testing.T) {
	tests := []struct {
		kind MistakeKind
		want string
		ok   bool
	}{
		{MistakeKindAPIError, "api_error", true},
		{MistakeKindInvalidToolCall, "invalid_tool_call", true},
		{MistakeKindToolExecutionFailed, "tool_execution_failed", true},
		{"", "", false},
		{"unknown", "unknown", false},
	}
	for _, tt := range tests {
		if got := string(tt.kind); got != tt.want {
			t.Errorf("string(%q) = %q, want %q", tt.kind, got, tt.want)
		}
		if got := tt.kind.Valid(); got != tt.ok {
			t.Errorf("%q.Valid() = %v, want %v", tt.kind, got, tt.ok)
		}
	}
}

// TestMistakeTrackerConsecutiveCountAcrossKinds checks that different failure
// kinds accumulate into one consecutive streak and are classified separately.
func TestMistakeTrackerConsecutiveCountAcrossKinds(t *testing.T) {
	tr := NewMistakeTracker(0)
	tr.Record(MistakeKindAPIError)
	tr.Record(MistakeKindInvalidToolCall)
	tr.Record(MistakeKindInvalidToolCall)
	tr.Record(MistakeKindToolExecutionFailed)

	if got := tr.Count(); got != 4 {
		t.Fatalf("Count() = %d, want 4", got)
	}
	for k, want := range map[MistakeKind]int{
		MistakeKindAPIError:            1,
		MistakeKindInvalidToolCall:     2,
		MistakeKindToolExecutionFailed: 1,
	} {
		if got := tr.CountFor(k); got != want {
			t.Errorf("CountFor(%q) = %d, want %d", k, got, want)
		}
	}

	// Zero and unknown kinds must not inflate the streak.
	tr.Record("")
	tr.Record("unrecognized")
	if got := tr.Count(); got != 4 {
		t.Fatalf("Count() after ignored kinds = %d, want 4", got)
	}
}

// TestMistakeTrackerCheckThreshold pins the configurable limit and the
// below/at-threshold transition, including the default of 3.
func TestMistakeTrackerCheckThreshold(t *testing.T) {
	tests := []struct {
		name  string
		limit int
	}{
		{"default", 0},
		{"negative falls back to default", -5},
		{"one", 1},
		{"five", 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := NewMistakeTracker(tt.limit)
			limit := tt.limit
			if limit <= 0 {
				limit = DefaultMistakeLimit
			}
			for i := 1; i < limit; i++ {
				tr.Record(MistakeKindAPIError)
				if stop, _ := tr.Check(); stop {
					t.Fatalf("Check() stopped after %d mistakes, want < %d", i, limit)
				}
			}
			tr.Record(MistakeKindInvalidToolCall)
			stop, action := tr.Check()
			if !stop {
				t.Fatalf("Check() did not stop at %d mistakes", limit)
			}
			if !action.Stop {
				t.Errorf("action.Stop = false, want true (no guidance injected)")
			}
			if action.Count != limit {
				t.Errorf("action.Count = %d, want %d", action.Count, limit)
			}
			if action.Kind != MistakeKindInvalidToolCall {
				t.Errorf("action.Kind = %q, want %q", action.Kind, MistakeKindInvalidToolCall)
			}
		})
	}
}

// TestMistakeTrackerGuidanceContinuesRun checks the injected-guidance path:
// a non-empty instruction turns a would-be stop into a continue+guidance
// action, and the callback sees the triggering kind and count.
func TestMistakeTrackerGuidanceContinuesRun(t *testing.T) {
	var gotKind MistakeKind
	var gotCount int
	guidance := func(k MistakeKind, n int) string {
		gotKind, gotCount = k, n
		return fmt.Sprintf("recover from %s after %d mistakes", k, n)
	}
	tr := NewMistakeTracker(2, guidance)

	tr.Record(MistakeKindAPIError)
	if stop, _ := tr.Check(); stop {
		t.Fatal("Check() stopped before the limit despite injected guidance")
	}
	tr.Record(MistakeKindToolExecutionFailed)
	stop, action := tr.Check()
	if stop {
		t.Fatal("Check() stopped at the limit even though guidance was injected")
	}
	if action.Stop {
		t.Error("action.Stop = true, want false (continue with guidance)")
	}
	want := "recover from tool_execution_failed after 2 mistakes"
	if action.Guidance != want {
		t.Errorf("action.Guidance = %q, want %q", action.Guidance, want)
	}
	if gotKind != MistakeKindToolExecutionFailed || gotCount != 2 {
		t.Errorf("guidance called with (%q, %d), want (%q, 2)", gotKind, gotCount, MistakeKindToolExecutionFailed)
	}
}

// TestMistakeTrackerSetGuidance covers replacement of the callback, nil
// removal, and the empty-result fallback to stop.
func TestMistakeTrackerSetGuidance(t *testing.T) {
	tr := NewMistakeTracker(1)
	tr.Record(MistakeKindAPIError)
	if stop, _ := tr.Check(); !stop {
		t.Fatal("Check() without guidance should stop at the limit")
	}

	tr.SetGuidance(func(MistakeKind, int) string { return "  try a different approach  " })
	stop, action := tr.Check()
	if stop || action.Stop || action.Guidance != "try a different approach" {
		t.Fatalf("continue action = (%v, %+v), want (false, guidance trimmed)", stop, action)
	}

	tr.SetGuidance(func(MistakeKind, int) string { return " \t " })
	if stop, _ := tr.Check(); !stop {
		t.Fatal("empty guidance should fall back to stop")
	}

	tr.SetGuidance(nil)
	if stop, _ := tr.Check(); !stop {
		t.Fatal("nil guidance should stop at the limit")
	}
}

// TestMistakeTrackerReset checks that a successful round clears the streak and
// the per-kind tally while keeping the limit and injected guidance.
func TestMistakeTrackerReset(t *testing.T) {
	guidance := func(k MistakeKind, n int) string { return "guidance:" + string(k) }
	tr := NewMistakeTracker(2, guidance)

	tr.Record(MistakeKindAPIError)
	tr.Record(MistakeKindAPIError)
	if _, action := tr.Check(); action.Guidance != "guidance:api_error" {
		t.Fatal("precondition: streak should have reached the limit with guidance")
	}

	tr.Reset()
	if got := tr.Count(); got != 0 {
		t.Fatalf("Count() after Reset = %d, want 0", got)
	}
	if got := tr.CountFor(MistakeKindAPIError); got != 0 {
		t.Fatalf("CountFor(api_error) after Reset = %d, want 0", got)
	}
	if stop, action := tr.Check(); stop || action != (MistakeAction{}) {
		t.Fatalf("Check() after Reset = (%v, %+v), want (false, zero)", stop, action)
	}

	tr.Record(MistakeKindInvalidToolCall)
	tr.Record(MistakeKindInvalidToolCall)
	stop, action := tr.Check()
	if stop || action.Guidance != "guidance:invalid_tool_call" {
		t.Fatalf("guidance not preserved across Reset: (%v, %+v)", stop, action)
	}
}

// TestMistakeTrackerConcurrentRecord drives many goroutines through Record,
// Check, read accessors and SetGuidance. With -race this catches any data race;
// without it the exact final total still asserts that no update was lost.
func TestMistakeTrackerConcurrentRecord(t *testing.T) {
	const (
		goroutines = 16
		perRoutine = 50
	)
	kinds := []MistakeKind{
		MistakeKindAPIError,
		MistakeKindInvalidToolCall,
		MistakeKindToolExecutionFailed,
	}
	guidance := func(k MistakeKind, n int) string { return "concurrent:" + string(k) }
	tr := NewMistakeTracker(goroutines * perRoutine)

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perRoutine; i++ {
				tr.Record(kinds[(g+i)%len(kinds)])
				_, _ = tr.Check()
				if i%17 == 0 {
					_ = tr.Count()
					_ = tr.CountFor(kinds[i%len(kinds)])
					tr.SetGuidance(guidance)
				}
			}
		}(g)
	}
	wg.Wait()

	want := goroutines * perRoutine
	if got := tr.Count(); got != want {
		t.Fatalf("Count() = %d, want %d (lost updates)", got, want)
	}
	total := 0
	for _, k := range kinds {
		total += tr.CountFor(k)
	}
	if total != want {
		t.Fatalf("sum(CountFor) = %d, want %d", total, want)
	}
}

// TestMistakeTrackerNilReceiver pins the zero-value/nil-receiver safety so a
// caller can hold a *MistakeTracker that was never constructed.
func TestMistakeTrackerNilReceiver(t *testing.T) {
	var tr *MistakeTracker
	tr.Record(MistakeKindAPIError)
	tr.SetGuidance(func(MistakeKind, int) string { return "x" })
	if got := tr.Count(); got != 0 {
		t.Errorf("nil Count() = %d, want 0", got)
	}
	if got := tr.CountFor(MistakeKindAPIError); got != 0 {
		t.Errorf("nil CountFor() = %d, want 0", got)
	}
	if stop, action := tr.Check(); stop || action != (MistakeAction{}) {
		t.Errorf("nil Check() = (%v, %+v), want (false, zero)", stop, action)
	}
	tr.Reset() // must not panic
}
