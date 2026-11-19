package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestGitStatusHelperProcess is re-executed as a fake git binary.
func TestGitStatusHelperProcess(t *testing.T) {
	if os.Getenv("ARGENT_GO_WANT_GIT_HELPER") != "1" {
		return
	}
	if os.Getenv("ARGENT_GO_GIT_HELPER_FAIL") == "1" {
		fmt.Println("fatal: not a git repository")
		os.Exit(1)
	}
	fmt.Println("## main")
	fmt.Println(" M a.txt")
	os.Exit(0)
}

func fakeGit(t *testing.T, fail bool) func() {
	t.Helper()
	previous := gitStatusCommand
	gitStatusCommand = func(ctx context.Context, _ string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestGitStatusHelperProcess", "--")
		cmd.Env = append(os.Environ(), "ARGENT_GO_WANT_GIT_HELPER=1")
		if fail {
			cmd.Env = append(cmd.Env, "ARGENT_GO_GIT_HELPER_FAIL=1")
		}
		return cmd
	}
	return func() { gitStatusCommand = previous }
}

func TestGitStatusReportsBranchAndChanges(t *testing.T) {
	defer fakeGit(t, false)()

	registry := New(t.TempDir(), true)
	out, err := registry.Run(context.Background(), "git_status", `{}`)
	if err != nil {
		t.Fatalf("git_status: %v", err)
	}
	if out != "## main\n M a.txt" {
		t.Fatalf("git_status = %q", out)
	}
}

func TestGitStatusSurfacesCommandFailure(t *testing.T) {
	defer fakeGit(t, true)()

	registry := New(t.TempDir(), true)
	out, err := registry.Run(context.Background(), "git_status", `{}`)
	if err == nil {
		t.Fatal("git_status hid the git failure")
	}
	if !strings.Contains(out, "not a git repository") {
		t.Fatalf("output = %q, want the git stderr", out)
	}
}
