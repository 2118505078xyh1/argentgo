package app

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// TestAbortDuringPromptStartCancelsPrompt exercises the Abort/Prompt-start
// interleave that used to lose the abort: the single-flight slot is claimed
// before the cancel func is registered, so an Abort landing in between saw a
// nil cancel and became a no-op, leaving the new prompt running uncancelled.
//
// The seam lets us stop the prompt exactly after it claimed the slot and
// registered its cancel func and call Abort at that point. With the slot claim
// and cancel registration merged into one critical section, Abort must see a
// fully registered cancel and cancel the prompt.
func TestAbortDuringPromptStartCancelsPrompt(t *testing.T) {
	t.Parallel()

	r, ctl := newSingleflightRunner(t)

	atClaim := make(chan struct{})
	proceed := make(chan struct{})
	var once sync.Once
	r.claimHook = func(got *runner) {
		if got != r {
			return
		}
		once.Do(func() {
			close(atClaim)
			<-proceed
		})
	}

	first := promptAsync(r, "first")
	<-atClaim // prompt has claimed the slot and registered its cancel func

	// The slot and its cancel func are always visible together; the
	// "busy but no cancel" intermediate state cannot be observed any more.
	r.mu.Lock()
	consistent := r.busy && r.cancel != nil
	r.mu.Unlock()
	if !consistent {
		t.Fatal("prompt claimed the single-flight slot without registering its cancel func")
	}

	r.Abort() // lands at prompt start; must cancel the just-started prompt
	close(proceed)

	res := awaitResult(t, first)
	if !errors.Is(res.err, context.Canceled) {
		t.Fatalf("prompt err = %v, want context.Canceled", res.err)
	}
	// The seam recorded the first prompt's cancelled ctx; drain it so it is
	// not mistaken for the second prompt's entry below.
	<-ctl.entered

	// The aborted run released the slot; a following prompt must get a fresh,
	// uncancelled context.
	second := promptAsync(r, "second")
	ctx2 := awaitEntered(t, ctl)
	if ctx2.Err() != nil {
		t.Fatalf("second prompt ctx cancelled by earlier abort: %v", ctx2.Err())
	}
	ctl.resume()
	if res := awaitResult(t, second); res.err != nil || res.text != "done" {
		t.Fatalf("second prompt = %+v, want done/nil", res)
	}
}
