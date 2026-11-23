package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestToolTimeoutSentinel 锁定 ErrToolTimeout 可被 errors.Is 匹配：
// agent dispatch 边界用它包装超时，下游护栏/审计据此按语义路由而非匹配文案。
func TestToolTimeoutSentinel(t *testing.T) {
	wrapped := fmt.Errorf("bash: %w", ErrToolTimeout)
	if !errors.Is(wrapped, ErrToolTimeout) {
		t.Fatal("wrapped timeout must match ErrToolTimeout via errors.Is")
	}
	if errors.Is(errors.New("other"), ErrToolTimeout) {
		t.Fatal("unrelated error must not match ErrToolTimeout")
	}
}

func TestClampTimeout(t *testing.T) {
	tests := []struct {
		name      string
		tool      string
		raw       time.Duration
		globalMax time.Duration
		want      time.Duration
	}{
		{"bash default", "bash", 0, 0, 120 * time.Second},
		{"fetch default", "fetch", 0, 0, 20 * time.Second},
		{"web_search default", "web_search", 0, 0, 20 * time.Second},
		{"git_status default", "git_status", 0, 0, 20 * time.Second},
		{"explicit value within range", "bash", 30 * time.Second, 0, 30 * time.Second},
		{"bash over per-tool max", "bash", 86400 * time.Second, 0, 3600 * time.Second},
		{"fetch over per-tool max", "fetch", 5 * time.Minute, 0, 45 * time.Second},
		{"git_status over per-tool max", "git_status", 5 * time.Minute, 0, 60 * time.Second},
		{"global cap on default", "bash", 0, 10 * time.Second, 10 * time.Second},
		{"global cap on explicit value", "bash", 600 * time.Second, 30 * time.Second, 30 * time.Second},
		{"min clamp", "bash", 100 * time.Millisecond, 0, time.Second},
		{"min clamp after global cap", "fetch", 0, 200 * time.Millisecond, time.Second},
		{"non-positive global cap ignored", "bash", 30 * time.Second, -time.Second, 30 * time.Second},
		{"untimed tools keep raw", "read", 5 * time.Second, 0, 5 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := clampTimeout(tt.tool, tt.raw, tt.globalMax); got != tt.want {
				t.Fatalf("clampTimeout(%q, %v, %v) = %v, want %v",
					tt.tool, tt.raw, tt.globalMax, got, tt.want)
			}
		})
	}
}

// TestSetMaxTimeoutCapsToolDeadline 用一个可观察的 deadline 检查
// 从注册表到工具的传递：假的 git 命令记录它收到的 context，
// 而不是检查内部字段。
func TestSetMaxTimeoutCapsToolDeadline(t *testing.T) {
	t.Setenv("ARGENT_GO_WANT_GIT_HELPER", "1")
	previous := gitStatusCommand
	var deadline time.Time
	gitStatusCommand = func(ctx context.Context, root string) *exec.Cmd {
		deadline, _ = ctx.Deadline()
		return exec.CommandContext(ctx, os.Args[0], "-test.run=TestGitStatusHelperProcess", "--")
	}
	defer func() { gitStatusCommand = previous }()

	for _, tt := range []struct {
		name    string
		cap     time.Duration
		wantMin time.Duration
		wantMax time.Duration
	}{
		// 下界必须容忍较慢的命令以及调度器/race 检测器的
		// 开销：重点在于默认值约为 20s 且未被钳制，
		// 而不是命令在某个特定的墙上时钟窗口内完成。
		{"default uncapped", 0, 10 * time.Second, 20 * time.Second},
		{"registry cap", 2 * time.Second, 0, 2 * time.Second},
	} {
		t.Run(tt.name, func(t *testing.T) {
			registry := New(t.TempDir(), true)
			registry.SetMaxTimeout(tt.cap)
			if _, err := registry.Run(context.Background(), "git_status", `{}`); err != nil {
				t.Fatalf("git_status: %v", err)
			}
			remaining := time.Until(deadline)
			if remaining < tt.wantMin || remaining > tt.wantMax {
				t.Fatalf("deadline remaining = %v, want between %v and %v", remaining, tt.wantMin, tt.wantMax)
			}
		})
	}
}
