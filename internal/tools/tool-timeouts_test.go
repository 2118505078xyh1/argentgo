package tools

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

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

// TestSetMaxTimeoutCapsToolDeadline checks the registry-to-tool plumbing with
// an observable deadline: the fake git command records the context it receives
// instead of inspecting internal fields.
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
		{"default uncapped", 0, 19 * time.Second, 20 * time.Second},
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
