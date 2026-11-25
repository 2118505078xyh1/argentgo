package app

import (
	"context"
	"strings"
	"testing"

	"example.com/argentgo/internal/cli"
	"example.com/argentgo/internal/session"
)

func TestExecuteAutoCompactsLongTranscriptUnlessDisabled(t *testing.T) {
	env, _ := testEnv(t)
	root := t.TempDir()
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: root})
	if err != nil {
		t.Fatal(err)
	}
	// 未配置阈值意味着编译默认值生效。
	if res.AutoCompactTokens != 0 || res.NoAutoCompact {
		t.Fatalf("unexpected compact settings: threshold=%d disabled=%v", res.AutoCompactTokens, res.NoAutoCompact)
	}
	store := testStore(t, res)

	build := func(title string) *session.Session {
		t.Helper()
		sess, err := store.NewSession(title)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 900; i++ {
			if err := sess.Append("user", map[string]any{"content": strings.Repeat("x", 200)}); err != nil {
				t.Fatal(err)
			}
		}
		return sess
	}
	run := func(settings Resolved, sess *session.Session) {
		t.Helper()
		settings.Chatter = &scriptedChatter{replies: []string{"done"}}
		if _, err := Execute(context.Background(), cli.IO{Env: env}, settings, sess, "prompt", nil); err != nil {
			t.Fatalf("Execute: %v", err)
		}
	}

	compacted := build("compact-default")
	run(res, compacted)
	if !hasCompaction(compacted) {
		t.Fatal("a long transcript must be compacted by the post-run step")
	}

	skipped := res
	skipped.NoAutoCompact = true
	kept := build("compact-skipped")
	run(skipped, kept)
	if hasCompaction(kept) {
		t.Fatal("--no-auto-compact must skip compaction after a real Execute")
	}
}
