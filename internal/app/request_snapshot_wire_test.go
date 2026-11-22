package app

import (
	"context"
	"testing"
)

func TestRequestSnapshotEnabledFromEnv(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"", false}, {"1", true}, {"true", true}, {"YES", true},
		{"  on  ", true}, {"0", false}, {"false", false}, {"maybe", false},
	}
	for _, tc := range cases {
		env := func(string) string { return tc.raw }
		if got := requestSnapshotEnabled(env); got != tc.want {
			t.Errorf("requestSnapshotEnabled(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
	if requestSnapshotEnabled(nil) {
		t.Fatal("nil env must leave request snapshots off")
	}
}

// TestWithRequestSnapshotDefaultNoop 验证默认（关闭或无会话）时 ctx 不被改动，
// 因此 llm 侧钩子是零副作用 no-op。
func TestWithRequestSnapshotDefaultNoop(t *testing.T) {
	base := context.Background()
	if got := withRequestSnapshot(base, nil, nil); got != base {
		t.Fatal("disabled wire must return the original ctx")
	}
	envOff := func(string) string { return "0" }
	if got := withRequestSnapshot(base, envOff, nil); got != base {
		t.Fatal("disabled wire must return the original ctx even with nil session")
	}
}
