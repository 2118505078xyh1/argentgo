package app

import (
	"testing"

	"example.com/argentgo/internal/cli"
	"example.com/argentgo/internal/tools"
)

func TestWorktreeToolsEnabled(t *testing.T) {
	cases := []struct {
		env  func(string) string
		want bool
	}{
		{nil, true},
		{func(string) string { return "" }, true},
		{func(string) string { return " 1 " }, true},
		{func(string) string { return "0" }, false},
		{func(string) string { return " 0 " }, false},
	}
	for _, tc := range cases {
		if got := worktreeToolsEnabled(tc.env); got != tc.want {
			t.Errorf("worktreeToolsEnabled = %v, want %v", got, tc.want)
		}
	}
}

func TestWireWorktreesInstallsThreeTools(t *testing.T) {
	r := tools.New(t.TempDir(), true)
	set := &toolset{base: &multiRootTools{regs: []*tools.Registry{r}}}
	wireWorktrees(cli.IO{Env: nil}, set)
	wireWorktrees(cli.IO{Env: nil}, set) // 幂等
	want := map[string]bool{"worktree_create": true, "worktree_list": true, "worktree_remove": true}
	seen := map[string]bool{}
	for _, name := range r.Names() {
		if want[name] {
			if seen[name] {
				t.Fatalf("%s 重复注册", name)
			}
			seen[name] = true
		}
	}
	if len(seen) != 3 {
		t.Fatalf("registered worktree tools = %v, want all three", seen)
	}
}

func TestWireWorktreesDisabled(t *testing.T) {
	r := tools.New(t.TempDir(), true)
	set := &toolset{base: &multiRootTools{regs: []*tools.Registry{r}}}
	wireWorktrees(cli.IO{Env: func(string) string { return "0" }}, set)
	for _, name := range r.Names() {
		if name == "worktree_create" || name == "worktree_list" || name == "worktree_remove" {
			t.Fatalf("worktree tool %q registered despite disable", name)
		}
	}
}
