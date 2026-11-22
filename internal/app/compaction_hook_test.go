package app

import (
	"context"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/hooks"
	"github.com/2118505078xyh1/argentgo/internal/llm"
)

func compactionHookMessages() []llm.Message {
	return []llm.Message{
		{Role: "system", Content: "rules"},
		{Role: "user", Content: "goal"},
		{Role: "assistant", Content: "step"},
		{Role: "tool", ToolCallID: "c1", Content: strings.Repeat("x", 400)},
		{Role: "assistant", Content: "continue"},
		{Role: "tool", ToolCallID: "c2", Content: strings.Repeat("y", 400)},
	}
}

func TestCompactionHookPreBlocksAndPostFires(t *testing.T) {
	env, _ := testEnv(t)
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	store := testStore(t, res)
	messages := compactionHookMessages()

	// 基线：无钩子时确实压缩。
	sess, _ := store.NewSession("compact-hook-baseline")
	for _, m := range messages {
		_ = sess.Append(m.Role, map[string]any{"content": m.Content, "tool_call_id": m.ToolCallID})
	}
	if _, changed := newSessionCompactor(res, sess).MaybeCompact(messages, 150); !changed {
		t.Fatal("baseline should compact")
	}

	// PreCompact blocked：消息不变、无压缩记录。
	sess2, _ := store.NewSession("compact-hook-block")
	for _, m := range messages {
		_ = sess2.Append(m.Role, map[string]any{"content": m.Content, "tool_call_id": m.ToolCallID})
	}
	var fired []hooks.Event
	comp := newSessionCompactor(res, sess2)
	comp.compactionHook = true
	comp.fireHook = func(_ context.Context, event hooks.Event, payload hooks.Payload) (hooks.Result, error) {
		fired = append(fired, event)
		if event == hooks.PreCompact {
			if payload.Trigger != "auto" {
				t.Fatalf("PreCompact trigger = %q", payload.Trigger)
			}
			return hooks.Result{Blocked: true, Message: "hook says no"}, nil
		}
		return hooks.Result{}, nil
	}
	out, changed := comp.MaybeCompact(messages, 150)
	if changed || len(out) != len(messages) {
		t.Fatalf("blocked compaction should return messages unchanged (changed=%v len=%d)", changed, len(out))
	}
	if hasCompaction(sess2) {
		t.Fatal("blocked compaction must not write a compaction record")
	}
	if len(fired) != 1 || fired[0] != hooks.PreCompact {
		t.Fatalf("expected only PreCompact to fire, got %v", fired)
	}
}

func TestCompactionHookPostFiresAfterCommit(t *testing.T) {
	env, _ := testEnv(t)
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	store := testStore(t, res)
	sess, _ := store.NewSession("compact-hook-post")
	messages := compactionHookMessages()
	for _, m := range messages {
		_ = sess.Append(m.Role, map[string]any{"content": m.Content, "tool_call_id": m.ToolCallID})
	}
	var fired []hooks.Event
	comp := newSessionCompactor(res, sess)
	comp.compactionHook = true
	comp.fireHook = func(_ context.Context, event hooks.Event, _ hooks.Payload) (hooks.Result, error) {
		fired = append(fired, event)
		return hooks.Result{}, nil
	}
	if _, changed := comp.MaybeCompact(messages, 150); !changed {
		t.Fatal("expected compaction")
	}
	if len(fired) != 2 || fired[0] != hooks.PreCompact || fired[1] != hooks.PostCompact {
		t.Fatalf("expected PreCompact then PostCompact, got %v", fired)
	}
}

func TestCompactionHookOffDoesNotFire(t *testing.T) {
	env, _ := testEnv(t)
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	store := testStore(t, res)
	sess, _ := store.NewSession("compact-hook-off")
	messages := compactionHookMessages()
	for _, m := range messages {
		_ = sess.Append(m.Role, map[string]any{"content": m.Content, "tool_call_id": m.ToolCallID})
	}
	comp := newSessionCompactor(res, sess)
	comp.fireHook = func(context.Context, hooks.Event, hooks.Payload) (hooks.Result, error) {
		t.Fatal("hook must not fire when compactionHook is false")
		return hooks.Result{}, nil
	}
	if _, changed := comp.MaybeCompact(messages, 150); !changed {
		t.Fatal("expected compaction")
	}
}
