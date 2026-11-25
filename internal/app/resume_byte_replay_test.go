package app

import (
	"testing"

	"example.com/argentgo/internal/session"
)

func TestResumeByteReplayEnabledFromEnv(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"", false}, {"1", true}, {"true", true}, {"YES", true},
		{"  on  ", true}, {"0", false}, {"false", false}, {"maybe", false},
	}
	for _, tc := range cases {
		env := func(string) string { return tc.raw }
		if got := resumeByteReplayEnabled(env); got != tc.want {
			t.Errorf("enabled(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
	if resumeByteReplayEnabled(nil) {
		t.Fatal("nil env must leave replay off")
	}
}

func TestShouldReplayCategory(t *testing.T) {
	if !shouldReplayCategory(string(session.RecoveryInterruptedPrompt)) {
		t.Fatal("interrupted prompt must be replayable")
	}
	for _, cat := range []string{"", "interrupted_turn", "degraded_history", "clean"} {
		if shouldReplayCategory(cat) {
			t.Errorf("category %q must not be replayable", cat)
		}
	}
}

func TestSelectReplayCandidate(t *testing.T) {
	snap := &session.ReplaySnapshot{
		Payload:    session.RequestSnapshotPayload{Protocol: "p", Model: "m", Body: []byte(`{"raw":1}`)},
		SnapshotID: "x",
	}
	cand := selectReplayCandidate(snap, string(session.RecoveryInterruptedPrompt), "m", "p")
	if cand == nil || string(cand.body) != `{"raw":1}` {
		t.Fatalf("valid candidate rejected: %+v", cand)
	}

	// 模型漂移 → 拒绝。
	if got := selectReplayCandidate(snap, string(session.RecoveryInterruptedPrompt), "other", "p"); got != nil {
		t.Fatal("model drift must reject replay")
	}
	// 协议漂移 → 拒绝。
	if got := selectReplayCandidate(snap, string(session.RecoveryInterruptedPrompt), "m", "other"); got != nil {
		t.Fatal("protocol drift must reject replay")
	}
	// 类别不符 → 拒绝。
	if got := selectReplayCandidate(snap, "clean", "m", "p"); got != nil {
		t.Fatal("clean session must not replay")
	}

	// 快照后有回复 → 拒绝。
	later := *snap
	later.HasLaterReply = true
	if got := selectReplayCandidate(&later, string(session.RecoveryInterruptedPrompt), "m", "p"); got != nil {
		t.Fatal("snapshot with later reply must reject")
	}
	// 快照后有压缩 → 拒绝。
	compacted := *snap
	compacted.HasCompaction = true
	if got := selectReplayCandidate(&compacted, string(session.RecoveryInterruptedPrompt), "m", "p"); got != nil {
		t.Fatal("snapshot before compaction must reject")
	}
}

func TestReplayAccessorsNilSafe(t *testing.T) {
	if replayBodyOf(nil) != nil || replayModelOf(nil) != "" || replayProtocolOf(nil) != "" {
		t.Fatal("nil candidate accessors must return zero values")
	}
}
