package app

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// TestAbortDuringPromptStartCancelsPrompt 复现以前会丢失 abort 的 Abort/Prompt-start
// 交错：single-flight 槽位被占用之后、取消函数注册之前，
// 若 Abort 落在中间，就会看到 nil 的 cancel，从而成为空操作，
// 使新提示未被取消而继续运行。
//
// 该接缝让我们能在提示刚占用槽位并注册其 cancel 函数之后，
// 恰好在那个时刻调用 Abort。当槽位占用与取消注册合并为同一个临界区后，
// Abort 必须看到一个
// 完整注册的 cancel，并取消该提示。
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
	<-atClaim // 提示已占用槽位并注册其 cancel 函数

	// 槽位与其 cancel 函数总是一起可见；
	// "忙但没有 cancel" 的中间状态再也观察不到了。
	r.mu.Lock()
	consistent := r.busy && r.cancel != nil
	r.mu.Unlock()
	if !consistent {
		t.Fatal("prompt claimed the single-flight slot without registering its cancel func")
	}

	r.Abort() // 落在提示开始处；必须取消刚启动的提示
	close(proceed)

	res := awaitResult(t, first)
	if !errors.Is(res.err, context.Canceled) {
		t.Fatalf("prompt err = %v, want context.Canceled", res.err)
	}
	// 接缝记录了第一个提示被取消的 ctx；抽取它，以免被
	// 误认为下面第二个提示的进入。
	<-ctl.entered

	// 被中止的运行释放了槽位；后续提示必须获得一个全新的、
	// 未被取消的 context。
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
