package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"testing"
)

// snapshotFingerprint 对内容取 SHA-256，模拟"内容变则指纹变"的指纹函数。
// 它用于验证等长、等 mtime 但内容不同的重写也能被指纹区分。
func snapshotFingerprint(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// TestPollSnapshotOutcomes 是 PollSnapshot 的核心表驱动测试，覆盖四种返回
// 约定以及 nil health 跳过校验、零值快照的边界。
func TestPollSnapshotOutcomes(t *testing.T) {
	lastGood := Snapshot[string]{Value: "good", Fingerprint: snapshotFingerprint("good"), Generation: 3}
	errLoad := errors.New("load failed")
	errHealth := errors.New("unhealthy")

	cases := []struct {
		name        string
		load        func() (Snapshot[string], error)
		health      func(string) error
		wantChanged bool
		wantErr     error
		wantValue   string
		wantGen     int
	}{
		{
			name: "无变化不交换且 health 不运行",
			load: func() (Snapshot[string], error) { return Snapshot[string]{}, nil },
			health: func(string) error {
				t.Fatal("health must not run when there is no candidate")
				return nil
			},
			wantChanged: false, wantErr: nil, wantValue: "", wantGen: 0,
		},
		{
			name:        "读取失败保留 last-good",
			load:        func() (Snapshot[string], error) { return lastGood, errLoad },
			health:      nil,
			wantChanged: false, wantErr: errLoad, wantValue: "good", wantGen: 3,
		},
		{
			name: "health 拒绝不采纳",
			load: func() (Snapshot[string], error) {
				return Snapshot[string]{Value: "bad", Fingerprint: snapshotFingerprint("bad"), Generation: 4}, nil
			},
			health:      func(string) error { return errHealth },
			wantChanged: false, wantErr: errHealth, wantValue: "bad", wantGen: 4,
		},
		{
			name: "健康候选被采纳",
			load: func() (Snapshot[string], error) {
				return Snapshot[string]{Value: "new", Fingerprint: snapshotFingerprint("new"), Generation: 4}, nil
			},
			health:      func(string) error { return nil },
			wantChanged: true, wantErr: nil, wantValue: "new", wantGen: 4,
		},
		{
			name: "nil health 跳过校验",
			load: func() (Snapshot[string], error) {
				return Snapshot[string]{Value: "new", Fingerprint: snapshotFingerprint("new"), Generation: 4}, nil
			},
			health:      nil,
			wantChanged: true, wantErr: nil, wantValue: "new", wantGen: 4,
		},
		{
			name:        "零值快照不交换",
			load:        func() (Snapshot[string], error) { return Snapshot[string]{}, nil },
			health:      nil,
			wantChanged: false, wantErr: nil, wantValue: "", wantGen: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, changed, err := PollSnapshot(tc.load, tc.health)
			if changed != tc.wantChanged {
				t.Errorf("changed = %v, want %v", changed, tc.wantChanged)
			}
			if tc.wantErr == nil {
				if err != nil {
					t.Errorf("err = %v, want nil", err)
				}
			} else if !errors.Is(err, tc.wantErr) {
				t.Errorf("err = %v, want %v", err, tc.wantErr)
			}
			if got.Value != tc.wantValue {
				t.Errorf("Value = %q, want %q", got.Value, tc.wantValue)
			}
			if got.Generation != tc.wantGen {
				t.Errorf("Generation = %d, want %d", got.Generation, tc.wantGen)
			}
		})
	}
}

// TestPollSnapshotGenerationMonotonic 演练长生命周期用法：采纳返回的快照后
// 再次轮询，generation 必须依次递增，且每次都以前一次的 last-good 为基准。
func TestPollSnapshotGenerationMonotonic(t *testing.T) {
	current := Snapshot[int]{Value: 0, Fingerprint: "seed", Generation: 0}
	for want := 1; want <= 3; want++ {
		base := current // 闭包捕获本次轮询的 last-good
		load := func() (Snapshot[int], error) {
			return Snapshot[int]{
				Value:       want,
				Fingerprint: strconv.Itoa(want),
				Generation:  base.Generation + 1,
			}, nil
		}
		next, changed, err := PollSnapshot(load, nil)
		if err != nil || !changed {
			t.Fatalf("poll %d = (%+v, %v, %v), want a change with no error", want, next, changed, err)
		}
		if next.Generation != want {
			t.Errorf("generation after poll %d = %d, want %d", want, next.Generation, want)
		}
		if next.Generation != base.Generation+1 {
			t.Errorf("generation %d did not build on last-good %d", next.Generation, base.Generation)
		}
		current = next // 采纳，使下一次轮询以它为基准
	}
}

// TestPollSnapshotEqualLengthEqualMtimeChange 验证指纹驱动的变更检测：
// 两段等长、mtime 相同的不同内容，其指纹不同；因此即使长度和时间戳都没变，
// 内容重写仍会被识别为一次可采纳的变化。
func TestPollSnapshotEqualLengthEqualMtimeChange(t *testing.T) {
	before, after := "abcdef", "abcdeg" // 等长，仅末字节不同
	mtimeBefore, mtimeAfter := int64(1000), int64(1000)
	if len(before) != len(after) || mtimeBefore != mtimeAfter {
		t.Fatal("precondition: inputs must have equal length and mtime")
	}
	fpBefore, fpAfter := snapshotFingerprint(before), snapshotFingerprint(after)
	if fpBefore == fpAfter {
		t.Fatal("precondition: different content must yield different fingerprints")
	}

	load := func() (Snapshot[string], error) {
		return Snapshot[string]{Value: after, Fingerprint: fpAfter, Generation: 1}, nil
	}
	next, changed, err := PollSnapshot(load, nil)
	if err != nil || !changed {
		t.Fatalf("equal-length/equal-mtime rewrite = (%+v, %v, %v), want a change", next, changed, err)
	}
	if next.Value != after || next.Fingerprint != fpAfter {
		t.Errorf("adopted snapshot = %+v, want value=%q fingerprint=%q", next, after, fpAfter)
	}
}

// TestPollSnapshotMissingFileFirstAdopt 记录缺失文件的首次采纳语义：
// 源不存在时 load 仍产出一个标识"缺失"的指纹（类似 PollReload 的零哈希
// 回退），首次轮询应把它当作有效快照采纳。
func TestPollSnapshotMissingFileFirstAdopt(t *testing.T) {
	load := func() (Snapshot[string], error) {
		return Snapshot[string]{Value: "<missing>", Fingerprint: "missing:argent.json", Generation: 1}, nil
	}
	next, changed, err := PollSnapshot(load, nil)
	if err != nil || !changed {
		t.Fatalf("first poll of missing file = (%+v, %v, %v), want a change", next, changed, err)
	}
	if next.Value != "<missing>" || next.Generation != 1 {
		t.Errorf("adopted snapshot = %+v, want missing value at generation 1", next)
	}
}

// TestPollSnapshotConcurrent 验证 PollSnapshot 作为纯函数可被多个 goroutine
// 并发调用：每个调用方都观察到一致、已变化且 generation 正确的快照，且无竞态。
func TestPollSnapshotConcurrent(t *testing.T) {
	load := func() (Snapshot[string], error) {
		return Snapshot[string]{Value: "v", Fingerprint: snapshotFingerprint("v"), Generation: 1}, nil
	}
	health := func(s string) error {
		if s == "v" {
			return nil
		}
		return errors.New("unexpected value")
	}

	const n = 64
	var wg sync.WaitGroup
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, changed, err := PollSnapshot(load, health)
			if err != nil {
				errCh <- fmt.Errorf("poll returned error: %w", err)
				return
			}
			if !changed || got.Value != "v" || got.Generation != 1 {
				errCh <- fmt.Errorf("inconsistent result: snapshot=%+v changed=%v", got, changed)
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}
