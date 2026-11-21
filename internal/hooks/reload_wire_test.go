package hooks

import (
	"errors"
	"fmt"
	"sync"
	"testing"
)

// wireSnap 构造一个仅用于断言的快照，指纹即其身份标识。
func wireSnap(fp string, gen int) ConfigSnapshot {
	return ConfigSnapshot{
		Config:      &Config{Commands: []Command{{Event: PreToolUse, Command: "echo " + fp}}},
		Fingerprint: fp,
		Generation:  gen,
	}
}

func TestSnapshotStoreSwapGenerationMonotonic(t *testing.T) {
	var store SnapshotStore
	tests := []struct {
		name    string
		gen     uint64
		snap    ConfigSnapshot
		wantOK  bool
		wantFP  string
		wantGen int
	}{
		{"首次采纳 gen=1", 1, wireSnap("fp-1", 1), true, "fp-1", 1},
		{"旧 gen=0 被拒", 0, wireSnap("stale-0", 0), false, "fp-1", 1},
		{"相等 gen=1 被拒", 1, wireSnap("same-1", 1), false, "fp-1", 1},
		{"更大 gen=2 采纳", 2, wireSnap("fp-2", 2), true, "fp-2", 2},
		{"旧 gen=1 不能覆盖新", 1, wireSnap("stale-1", 1), false, "fp-2", 2},
		{"旧 gen=2（相等）仍被拒", 2, wireSnap("same-2", 2), false, "fp-2", 2},
		{"gen=3 采纳", 3, wireSnap("配置-3", 3), true, "配置-3", 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := store.Swap(tt.snap, tt.gen); got != tt.wantOK {
				t.Fatalf("Swap(gen=%d) = %v, want %v", tt.gen, got, tt.wantOK)
			}
			loaded := store.Load()
			if loaded.Fingerprint != tt.wantFP || loaded.Generation != tt.wantGen {
				t.Fatalf("Load() = (fp=%q, gen=%d), want (%q, %d)",
					loaded.Fingerprint, loaded.Generation, tt.wantFP, tt.wantGen)
			}
			// 当前快照与 last-good 始终一致。
			if good := store.LastGood(); good != loaded {
				t.Fatalf("LastGood() = %+v, Load() = %+v", good, loaded)
			}
		})
	}
}

func TestSnapshotStoreSwapPreservesSnapshotFields(t *testing.T) {
	var store SnapshotStore
	snap := wireSnap("own-generation", 3)
	if !store.Swap(snap, 100) {
		t.Fatal("first Swap should succeed")
	}
	got := store.Load()
	if got.Fingerprint != snap.Fingerprint || got.Generation != snap.Generation {
		t.Fatalf("Swap rewrote snapshot fields: got (fp=%q, gen=%d), want (fp=%q, gen=%d)",
			got.Fingerprint, got.Generation, snap.Fingerprint, snap.Generation)
	}
	if len(got.Config.Commands) != 1 || got.Config.Commands[0].Command != "echo own-generation" {
		t.Fatalf("Swap lost config: %+v", got.Config)
	}
}

func TestSnapshotStoreZeroValue(t *testing.T) {
	var store SnapshotStore
	if !isEmptySnapshot(store.Load()) {
		t.Fatalf("zero store Load() = %+v, want empty", store.Load())
	}
	if !isEmptySnapshot(store.LastGood()) {
		t.Fatalf("zero store LastGood() = %+v, want empty", store.LastGood())
	}
	if err := store.LastError(); err != nil {
		t.Fatalf("zero store LastError() = %v, want nil", err)
	}
	// 代数从 0 起：gen=0 不是"更大"，必须拒绝；gen=1 才允许首次采纳。
	if store.Swap(wireSnap("zero", 0), 0) {
		t.Fatal("gen=0 must be rejected on a zero-value store")
	}
	if !store.Swap(wireSnap("one", 1), 1) {
		t.Fatal("gen=1 must be accepted on a zero-value store")
	}
}

func TestSnapshotStoreLastGoodSurvivesFailure(t *testing.T) {
	var store SnapshotStore
	good := wireSnap("last-good", 1)
	if !store.Swap(good, 7) {
		t.Fatal("first Swap should succeed")
	}

	failure := errors.New("poll failed")
	store.NoteFailure(failure)

	if got := store.LastGood(); got != good {
		t.Fatalf("LastGood() after failure = %+v, want %+v", got, good)
	}
	if got := store.Load(); got != good {
		t.Fatalf("Load() after failure = %+v, want %+v (failure must not touch the snapshot)", got, good)
	}
	if !errors.Is(store.LastError(), failure) {
		t.Fatalf("LastError() = %v, want the recorded failure", store.LastError())
	}

	// 记账后代数不变：旧 gen 仍被拒，更高的 gen 仍能采纳。
	if store.Swap(wireSnap("stale", 2), 6) {
		t.Fatal("stale gen should still be rejected after a failure")
	}
	next := wireSnap("next-good", 2)
	if !store.Swap(next, 8) {
		t.Fatal("higher gen should still be accepted after a failure")
	}
	if got := store.LastGood(); got != next {
		t.Fatalf("LastGood() after new Swap = %+v, want %+v", got, next)
	}
	// 成功 Swap 不清除失败记录。
	if !errors.Is(store.LastError(), failure) {
		t.Fatalf("Swap cleared LastError(): %v", store.LastError())
	}
}

func TestSnapshotStoreNoteFailureOnZeroValue(t *testing.T) {
	var store SnapshotStore
	want := errors.New("early failure")
	store.NoteFailure(want)
	if !isEmptySnapshot(store.Load()) || !isEmptySnapshot(store.LastGood()) {
		t.Fatalf("NoteFailure changed the snapshot: Load=%+v LastGood=%+v", store.Load(), store.LastGood())
	}
	if !errors.Is(store.LastError(), want) {
		t.Fatalf("LastError() = %v, want %v", store.LastError(), want)
	}
	store.NoteFailure(nil)
	if err := store.LastError(); err != nil {
		t.Fatalf("LastError() after NoteFailure(nil) = %v, want nil", err)
	}
}

func TestValidateBeforeAdopt(t *testing.T) {
	trust := NewTrustStore()
	trust.Trust("hooks/a.json", Digest([]byte(trustedDocument)))
	cjkTrust := NewTrustStore()
	cjkTrust.Trust("配置/钩子.json", Digest([]byte(trustedDocument)))

	tests := []struct {
		name    string
		snap    ConfigSnapshot
		trust   *TrustStore
		wantErr bool
	}{
		{"零值快照放行", ConfigSnapshot{}, trust, false},
		{"nil Config 放行", ConfigSnapshot{Fingerprint: "fp"}, trust, false},
		{"无逐源信息时放行（信任已在上游校验）", wireSnap("fp", 1), trust, false},
		{"nil 信任存储且无源信息时放行", wireSnap("fp", 1), nil, false},
		{"CJK 指纹的快照同样放行", wireSnap("配置/钩子.json", 1), cjkTrust, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateBeforeAdopt(tt.snap, tt.trust)
			if tt.wantErr && !errors.Is(err, ErrUntrusted) {
				t.Fatalf("ValidateBeforeAdopt() = %v, want ErrUntrusted", err)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("ValidateBeforeAdopt() = %v, want nil", err)
			}
		})
	}
}

func TestValidateTrustEvidence(t *testing.T) {
	digest := Digest([]byte(trustedDocument))
	trust := NewTrustStore()
	trust.Trust("hooks/a.json", digest)
	cjkTrust := NewTrustStore()
	// 授予键用反斜杠，核验凭据用正斜杠，验证归一化后仍匹配。
	cjkTrust.Trust(`配置\钩子.json`, digest)

	tests := []struct {
		name    string
		entries []TrustEntry
		trust   *TrustStore
		wantErr bool
	}{
		{"空凭据放行（源信息不可得）", nil, trust, false},
		{"授信条目放行", []TrustEntry{{Path: "hooks/a.json", Digest: digest}}, trust, false},
		{"nil 信任存储拒绝", []TrustEntry{{Path: "hooks/a.json", Digest: digest}}, nil, true},
		{"摘要变化拒绝", []TrustEntry{{Path: "hooks/a.json", Digest: "00"}}, trust, true},
		{"新键拒绝", []TrustEntry{{Path: "hooks/new.json", Digest: digest}}, trust, true},
		{"CJK 分隔符归一化放行", []TrustEntry{{Path: "配置/钩子.json", Digest: digest}}, cjkTrust, false},
		{"CJK 未授信键拒绝", []TrustEntry{{Path: "未授信/钩子.json", Digest: digest}}, cjkTrust, true},
		{
			"混合条目有一条未授信即拒绝",
			[]TrustEntry{{Path: "hooks/a.json", Digest: digest}, {Path: "hooks/b.json", Digest: digest}},
			trust, true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTrustEvidence(tt.entries, tt.trust)
			if tt.wantErr && !errors.Is(err, ErrUntrusted) {
				t.Fatalf("validateTrustEvidence() = %v, want ErrUntrusted", err)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("validateTrustEvidence() = %v, want nil", err)
			}
		})
	}
}

func TestSnapshotStoreConcurrentSwapAndLoad(t *testing.T) {
	var store SnapshotStore
	if !store.Swap(wireSnap("base", 1), 1) {
		t.Fatal("baseline Swap should succeed")
	}

	const goroutines = 8
	const iterations = 200
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				fp := fmt.Sprintf("g%d-i%d", g, i)
				// 多个 goroutine 故意使用相同 gen，覆盖单调拒绝路径。
				gen := uint64(2 + i)
				store.Swap(wireSnap(fp, i+2), gen)
				_ = store.Load()
				_ = store.LastGood()
				store.NoteFailure(errors.New(fp))
				_ = store.LastError()
			}
		}(g)
	}
	wg.Wait()

	loaded := store.Load()
	if isEmptySnapshot(loaded) {
		t.Fatal("snapshot became empty after concurrent access")
	}
	if good := store.LastGood(); good != loaded {
		t.Fatalf("LastGood() = %+v, Load() = %+v after concurrent access", good, loaded)
	}
}
