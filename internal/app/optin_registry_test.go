package app

import (
	"sync"
	"testing"
)

// optInWant 以默认全 false 的映射为基线，仅把 on 中的键设为 true，
// 让表驱动用例只需声明"哪些开关应开启"。
func optInWant(on ...OptInKey) map[OptInKey]bool {
	m := DefaultOptIns()
	for _, key := range on {
		m[key] = true
	}
	return m
}

func TestOptInRegistrySetEnabled(t *testing.T) {
	type setOp struct {
		key     OptInKey
		enabled bool
	}
	tests := []struct {
		name   string
		ops    []setOp
		checks map[OptInKey]bool
	}{
		{"unset key is off", nil, map[OptInKey]bool{OptInRefusalReason: false}},
		{"set true", []setOp{{OptInRefusalReason, true}}, map[OptInKey]bool{OptInRefusalReason: true}},
		{"set false stays off", []setOp{{OptInUntrustedResult, false}}, map[OptInKey]bool{OptInUntrustedResult: false}},
		{"last write wins", []setOp{{OptInContextPressure, true}, {OptInContextPressure, false}}, map[OptInKey]bool{OptInContextPressure: false}},
		{"keys are independent", []setOp{{OptInRefusalReason, true}}, map[OptInKey]bool{
			OptInRefusalReason:   true,
			OptInUntrustedResult: false,
		}},
		{"all keys set", []setOp{
			{OptInRefusalReason, true},
			{OptInUntrustedResult, true},
			{OptInContextPressure, true},
			{OptInUntrustedBoundary, true},
			{OptInAttemptIsolation, true},
			{OptInCompactionFidelity, true},
			{OptInKeepImplicitContext, true},
			{OptInPreserveSummarySections, true},
			{OptInApplyPatch, true},
			{OptInGrepEx, true},
			{OptInCliHints, true},
		}, optInWant(OptInRefusalReason, OptInUntrustedResult, OptInContextPressure, OptInUntrustedBoundary,
			OptInAttemptIsolation, OptInCompactionFidelity, OptInKeepImplicitContext, OptInPreserveSummarySections,
			OptInApplyPatch, OptInGrepEx, OptInCliHints)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var r OptInRegistry
			for _, op := range tt.ops {
				r.Set(op.key, op.enabled)
			}
			for key, want := range tt.checks {
				if got := r.Enabled(key); got != want {
					t.Errorf("Enabled(%q) = %v, want %v", key, got, want)
				}
			}
		})
	}
}

func TestOptInRegistrySnapshotIsCopy(t *testing.T) {
	var r OptInRegistry
	r.Set(OptInRefusalReason, true)
	r.Set(OptInUntrustedResult, false)

	snap := r.Snapshot()
	if len(snap) != 2 || !snap[OptInRefusalReason] || snap[OptInUntrustedResult] {
		t.Fatalf("Snapshot() = %v, want two keys with refusal=true", snap)
	}

	// 修改副本不应影响注册表内部状态。
	snap[OptInRefusalReason] = false
	snap[OptInContextPressure] = true
	delete(snap, OptInUntrustedResult)

	again := r.Snapshot()
	if len(again) != 2 || !again[OptInRefusalReason] || again[OptInUntrustedResult] {
		t.Fatalf("Snapshot() after mutating copy = %v, want original state", again)
	}
	if _, leaked := again[OptInContextPressure]; leaked {
		t.Fatalf("Snapshot() leaked a key added only to the copy: %v", again)
	}
}

func TestOptInRegistryReset(t *testing.T) {
	var r OptInRegistry
	for _, key := range allOptInKeys {
		r.Set(key, true)
	}
	r.Reset()

	for _, key := range allOptInKeys {
		if r.Enabled(key) {
			t.Errorf("Enabled(%q) after Reset = true, want false", key)
		}
	}
	if snap := r.Snapshot(); len(snap) != 0 {
		t.Fatalf("Snapshot() after Reset = %v, want empty", snap)
	}

	// Reset 之后注册表仍可正常使用。
	r.Set(OptInUntrustedBoundary, true)
	if !r.Enabled(OptInUntrustedBoundary) {
		t.Fatal("Set after Reset did not take effect")
	}
}

func TestOptInRegistryZeroValue(t *testing.T) {
	var r OptInRegistry

	if r.Enabled(OptInRefusalReason) {
		t.Fatal("zero-value Enabled = true, want false")
	}
	snap := r.Snapshot()
	if snap == nil || len(snap) != 0 {
		t.Fatalf("zero-value Snapshot() = %v, want non-nil empty map", snap)
	}

	r.Set(OptInRefusalReason, true)
	if !r.Enabled(OptInRefusalReason) {
		t.Fatal("Set on zero value did not take effect")
	}
	// 不同接收者的零值互不共享状态。
	var other OptInRegistry
	if other.Enabled(OptInRefusalReason) {
		t.Fatal("zero values share state")
	}
}

func TestDefaultOptInsAllFalse(t *testing.T) {
	got := DefaultOptIns()
	if len(got) != len(allOptInKeys) {
		t.Fatalf("DefaultOptIns() has %d keys, want %d", len(got), len(allOptInKeys))
	}
	for _, key := range allOptInKeys {
		if enabled, ok := got[key]; !ok || enabled {
			t.Errorf("DefaultOptIns()[%q] = (%v, %v), want (false, true)", key, enabled, ok)
		}
	}

	// 返回的映射是副本，修改不影响后续调用。
	got[OptInRefusalReason] = true
	if DefaultOptIns()[OptInRefusalReason] {
		t.Fatal("DefaultOptIns() returns a shared map")
	}
}

func TestParseOptInEnv(t *testing.T) {
	tests := []struct {
		name   string
		nilEnv bool
		value  string
		want   map[OptInKey]bool
	}{
		{"nil env is all false", true, "", optInWant()},
		{"empty value is all false", false, "", optInWant()},
		{"only commas is all false", false, ",,", optInWant()},
		{"single key", false, "refusal_reason", optInWant(OptInRefusalReason)},
		{"two keys", false, "refusal_reason,untrusted_result", optInWant(OptInRefusalReason, OptInUntrustedResult)},
		{"whitespace trimmed", false, "  context_pressure , untrusted_boundary  ", optInWant(OptInContextPressure, OptInUntrustedBoundary)},
		{"unknown key ignored", false, "bogus,refusal_reason", optInWant(OptInRefusalReason)},
		{"only unknown keys", false, "nope,nada", optInWant()},
		{"duplicate key", false, "refusal_reason,refusal_reason", optInWant(OptInRefusalReason)},
		{"empty segments ignored", false, "refusal_reason,,untrusted_result,", optInWant(OptInRefusalReason, OptInUntrustedResult)},
		{"CJK token ignored", false, "拒绝理由,refusal_reason", optInWant(OptInRefusalReason)},
		{"case sensitive", false, "REFUSAL_REASON", optInWant()},
		{"all keys", false, "refusal_reason,untrusted_result,context_pressure,untrusted_boundary," +
			"attempt_isolation,compaction_fidelity,keep_implicit_context,preserve_summary_sections," +
			"apply_patch,grep_ex,cli_hints",
			optInWant(OptInRefusalReason, OptInUntrustedResult, OptInContextPressure, OptInUntrustedBoundary,
				OptInAttemptIsolation, OptInCompactionFidelity, OptInKeepImplicitContext, OptInPreserveSummarySections,
				OptInApplyPatch, OptInGrepEx, OptInCliHints)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got map[OptInKey]bool
			if tt.nilEnv {
				got = ParseOptInEnv(nil)
			} else {
				got = ParseOptInEnv(func(string) string { return tt.value })
			}
			if len(got) != len(tt.want) {
				t.Fatalf("ParseOptInEnv() = %v, want %v", got, tt.want)
			}
			for key, want := range tt.want {
				if got[key] != want {
					t.Errorf("ParseOptInEnv()[%q] = %v, want %v", key, got[key], want)
				}
			}
		})
	}
}

func TestParseOptInEnvIsPure(t *testing.T) {
	// 记录 env 被查询的名字与次数，证明只读固定的单个变量且只读一次。
	var queried []string
	env := func(name string) string {
		queried = append(queried, name)
		return "refusal_reason,context_pressure"
	}
	first := ParseOptInEnv(env)
	if len(queried) != 1 || queried[0] != "ARGENT_OPT_INS" {
		t.Fatalf("env queried %v, want exactly [ARGENT_OPT_INS]", queried)
	}

	// 修改第一次结果不应影响第二次，且两次结果一致。
	first[OptInRefusalReason] = false
	first[OptInUntrustedResult] = true
	second := ParseOptInEnv(env)
	if !second[OptInRefusalReason] || !second[OptInContextPressure] {
		t.Fatalf("second ParseOptInEnv() = %v, want refusal/context on", second)
	}
	if second[OptInUntrustedResult] {
		t.Fatalf("second ParseOptInEnv() mutated by first: %v", second)
	}
}

func TestParseOptInEnvIgnoresProcessEnvironment(t *testing.T) {
	// 即使进程环境里设置了该变量，结果也只由传入的 env 函数决定。
	t.Setenv("ARGENT_OPT_INS", "refusal_reason")
	got := ParseOptInEnv(func(string) string { return "" })
	if got[OptInRefusalReason] {
		t.Fatalf("ParseOptInEnv() read process environment: %v", got)
	}
}

func TestOptInRegistryConcurrentSetEnabled(t *testing.T) {
	var r OptInRegistry
	keys := []OptInKey{OptInRefusalReason, OptInUntrustedResult, OptInContextPressure, OptInUntrustedBoundary}
	const (
		writers    = 8
		readers    = 4
		iterations = 200
	)

	stop := make(chan struct{})
	var writeWG, readWG sync.WaitGroup
	for w := 0; w < writers; w++ {
		writeWG.Add(1)
		go func(w int) {
			defer writeWG.Done()
			for i := 0; i < iterations; i++ {
				key := keys[(w+i)%len(keys)]
				r.Set(key, i%2 == 0)
				_ = r.Enabled(key)
				_ = r.Snapshot()
			}
		}(w)
	}
	for rd := 0; rd < readers; rd++ {
		readWG.Add(1)
		go func() {
			defer readWG.Done()
			for {
				select {
				case <-stop:
					return
				default:
					for _, key := range keys {
						_ = r.Enabled(key)
					}
					_ = r.Snapshot()
				}
			}
		}()
	}
	// 写 goroutine 全部结束后停止读 goroutine，再等待两者收尾。
	writeWG.Wait()
	close(stop)
	readWG.Wait()

	// 并发阶段只检验无 panic / 无数据竞争；用确定性操作验证最终状态一致。
	for _, key := range keys {
		r.Set(key, true)
	}
	for _, key := range keys {
		if !r.Enabled(key) {
			t.Errorf("Enabled(%q) after deterministic Set = false, want true", key)
		}
	}
	r.Reset()
	if snap := r.Snapshot(); len(snap) != 0 {
		t.Fatalf("Snapshot() after Reset = %v, want empty", snap)
	}
}
