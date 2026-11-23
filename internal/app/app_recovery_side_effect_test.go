package app

import (
	"strings"
	"testing"
)

// TestInterruptedSideEffectNotice 锁定 #20 的默认路径：interrupted/degraded 公告
// 默认带上副作用告示；=0 回退时逐字节不变；干净会话不产生凭空告示；
// NeedsConfirmation 完全不被触碰。
func TestInterruptedSideEffectNotice(t *testing.T) {
	t.Run("默认开启-中断提示", func(t *testing.T) {
		d := recoveryDecisionFor(recoveryMessages("interrupted_prompt"))
		base := d.Notice
		withInterruptedSideEffectNotice(&d, nil)
		if !strings.HasPrefix(d.Notice, base) || d.Notice == base {
			t.Fatalf("告示未追加到公告末尾: %q", d.Notice)
		}
		if !strings.Contains(d.Notice, interruptedSideEffectNotice) {
			t.Fatalf("缺少副作用告示: %q", d.Notice)
		}
		if d.NeedsConfirmation {
			t.Fatal("中断提示仍应可自动继续")
		}
		if len(interruptedSideEffectNotice) > 256 {
			t.Fatalf("告示未做到有界: %d bytes", len(interruptedSideEffectNotice))
		}
	})

	t.Run("默认开启-中断轮次", func(t *testing.T) {
		d := recoveryDecisionFor(recoveryMessages("interrupted_turn"))
		withInterruptedSideEffectNotice(&d, nil)
		if !strings.Contains(d.Notice, interruptedSideEffectNotice) {
			t.Fatalf("缺少副作用告示: %q", d.Notice)
		}
	})

	t.Run("默认开启-降级历史", func(t *testing.T) {
		d := recoveryDecisionFor(recoveryMessages("degraded_history"))
		base := d.Notice
		confirmed := d.NeedsConfirmation
		withInterruptedSideEffectNotice(&d, nil)
		if !strings.HasPrefix(d.Notice, base) || d.NeedsConfirmation != confirmed {
			t.Fatalf("降级历史告示/确认门被改变: %+v", d)
		}
	})

	t.Run("等于0回退逐字节不变", func(t *testing.T) {
		env := envFrom(map[string]string{interruptedSideEffectEnv: " 0 "})
		d := recoveryDecisionFor(recoveryMessages("interrupted_turn"))
		base := d.Notice
		withInterruptedSideEffectNotice(&d, env)
		if d.Notice != base {
			t.Fatalf("回退路径改变了公告: %q, want %q", d.Notice, base)
		}
	})

	t.Run("干净会话无凭空告示", func(t *testing.T) {
		for _, category := range []string{"clean", ""} {
			d := recoveryDecisionFor(recoveryMessages(category))
			withInterruptedSideEffectNotice(&d, nil)
			if d.Notice != "" {
				t.Fatalf("%s 会话产生了凭空告示: %q", category, d.Notice)
			}
		}
	})

	t.Run("幂等", func(t *testing.T) {
		d := recoveryDecisionFor(recoveryMessages("interrupted_prompt"))
		withInterruptedSideEffectNotice(&d, nil)
		once := d.Notice
		withInterruptedSideEffectNotice(&d, nil)
		if d.Notice != once {
			t.Fatalf("重复追加产生了不同公告: %q", d.Notice)
		}
	})
}
