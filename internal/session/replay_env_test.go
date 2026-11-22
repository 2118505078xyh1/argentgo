package session

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

func testReplayEnv() ReplayEnv {
	return ReplayEnv{
		Version:     replayEnvVersion,
		Seed:        0x1234abcd,
		Start:       time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC),
		ImplVersion: "v0.8.1",
	}
}

// TestEnvFingerprintStableAndVersioned 验证环境指纹的确定性、字段敏感性与
// 时区无关性：同环境同指纹，任一字段变化则指纹变化，文本带版本前缀。
func TestEnvFingerprintStableAndVersioned(t *testing.T) {
	base := testReplayEnv()
	want := EnvFingerprint(base)
	if got := EnvFingerprint(testReplayEnv()); got != want {
		t.Fatal("same env produced different fingerprints")
	}

	cases := []struct {
		name   string
		mutate func(*ReplayEnv)
	}{
		{"seed", func(e *ReplayEnv) { e.Seed++ }},
		{"version", func(e *ReplayEnv) { e.Version++ }},
		{"impl", func(e *ReplayEnv) { e.ImplVersion = "v0.8.2" }},
		{"start", func(e *ReplayEnv) { e.Start = e.Start.Add(time.Nanosecond) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			other := testReplayEnv()
			tc.mutate(&other)
			if got := EnvFingerprint(other); got == want {
				t.Fatalf("%s change did not affect fingerprint", tc.name)
			}
		})
	}

	text := EnvFingerprintText(base)
	if !strings.HasPrefix(text, "replay-env-v1:") {
		t.Fatalf("text = %q, want version prefix", text)
	}
}

// TestEnvFingerprintTimezoneIndependent 确认同一时刻的不同时区表示得到相同
// 指纹，避免因机器时区不同而产生的假失配。
func TestEnvFingerprintTimezoneIndependent(t *testing.T) {
	utc := testReplayEnv()
	shanghai := testReplayEnv()
	shanghai.Start = utc.Start.In(time.FixedZone("CST", 8*3600))
	if EnvFingerprint(utc) != EnvFingerprint(shanghai) {
		t.Fatal("same instant in different zones produced different fingerprints")
	}
}

// TestVirtualClockDeterministic 确认虚拟时钟只在 Advance 时前进，且不受真实
// 时间流逝影响。负增量被忽略。
func TestVirtualClockDeterministic(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := NewVirtualClock(start)
	if got := clock.Now(); !got.Equal(start) {
		t.Fatalf("Now = %v, want %v", got, start)
	}

	clock.Advance(2 * time.Second)
	want := start.Add(2 * time.Second)
	if got := clock.Now(); !got.Equal(want) {
		t.Fatalf("Now = %v, want %v", got, want)
	}
	clock.Advance(-time.Hour)
	if got := clock.Now(); !got.Equal(want) {
		t.Fatalf("negative Advance changed clock to %v", got)
	}
}

// TestSeededRandomDeterministic 确认同种子产生同序列、不同种子产生不同序列。
func TestSeededRandomDeterministic(t *testing.T) {
	a := NewSeededRandom(42)
	b := NewSeededRandom(42)
	c := NewSeededRandom(43)
	for i := 0; i < 20; i++ {
		if x, y := a.Next(), b.Next(); x != y {
			t.Fatalf("draw %d: %d != %d", i, x, y)
		}
	}
	if x, y := NewSeededRandom(42).Next(), c.Next(); x == y {
		t.Fatal("different seeds produced the same first draw")
	}
	if f := NewSeededRandom(7).Float64(); f < 0 || f >= 1 {
		t.Fatalf("Float64 = %v, want [0,1)", f)
	}
}

// TestSeqCollectorStrictOrdering 验证序号收集器把乱序输入严格重排，并在遇到
// 缺口时停止投递，重复或过旧的序号被拒绝。
func TestSeqCollectorStrictOrdering(t *testing.T) {
	collector := NewSeqCollector(0)
	if err := collector.Add(2, []byte("two")); err != nil {
		t.Fatalf("Add(2): %v", err)
	}
	if err := collector.Add(0, []byte("zero")); err != nil {
		t.Fatalf("Add(0): %v", err)
	}
	if err := collector.Add(1, []byte("one")); err != nil {
		t.Fatalf("Add(1): %v", err)
	}

	got := collector.Drain()
	want := [][]byte{[]byte("zero"), []byte("one"), []byte("two")}
	if len(got) != len(want) {
		t.Fatalf("Drain len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Errorf("Drain[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	if err := collector.Add(0, []byte("old")); !errors.Is(err, ErrSeqOutOfOrder) {
		t.Errorf("Add(0) err = %v, want ErrSeqOutOfOrder", err)
	}
	if err := collector.Add(3, []byte("three")); err != nil {
		t.Fatalf("Add(3): %v", err)
	}
	if err := collector.Add(3, []byte("again")); !errors.Is(err, ErrSeqOutOfOrder) {
		t.Errorf("duplicate Add(3) err = %v, want ErrSeqOutOfOrder", err)
	}
}

// TestSeqCollectorStopsAtGap 确认缺口之后的条目不会被提前投递。
func TestSeqCollectorStopsAtGap(t *testing.T) {
	collector := NewSeqCollector(4)
	if err := collector.Add(1, []byte("one")); err != nil {
		t.Fatalf("Add(1): %v", err)
	}
	if got := collector.Drain(); len(got) != 0 {
		t.Fatalf("Drain before gap = %q, want none", got)
	}
	if err := collector.Add(0, []byte("zero")); err != nil {
		t.Fatalf("Add(0): %v", err)
	}
	got := collector.Drain()
	if len(got) != 2 || string(got[0]) != "zero" || string(got[1]) != "one" {
		t.Fatalf("Drain after gap = %q, want [zero one]", got)
	}
}
