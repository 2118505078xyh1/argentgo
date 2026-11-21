package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// guardTestTree 在临时目录下创建若干文件，返回根目录。
func guardTestTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestCheckpointGuardValidateRestored(t *testing.T) {
	root := guardTestTree(t, map[string]string{"file.txt": "hello"})
	g := CheckpointGuard{SnapshotsEnabled: true}

	status, problems := g.Validate(root, "file.txt")
	if status != RestoreOK {
		t.Fatalf("status = %q, want %q (problems: %v)", status, RestoreOK, problems)
	}
	if len(problems) != 0 {
		t.Fatalf("problems = %v, want none", problems)
	}
}

func TestCheckpointGuardValidateEscapeFails(t *testing.T) {
	root := guardTestTree(t, map[string]string{"file.txt": "hello"})
	// 在 baseDir 之外放一个真实存在的文件，排除"不存在"干扰，专门验证逃逸拒绝。
	outside := filepath.Join(filepath.Dir(root), "outside-guard-target.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(outside)

	g := CheckpointGuard{SnapshotsEnabled: true}
	for _, tc := range []struct {
		name   string
		target string
	}{
		{"relative-parent", filepath.Join("..", "outside-guard-target.txt")},
		{"absolute-outside", outside},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, problems := g.Validate(root, tc.target)
			if status != RestoreValidationFailed {
				t.Fatalf("status = %q, want %q (problems: %v)", status, RestoreValidationFailed, problems)
			}
			if len(problems) == 0 || !strings.Contains(strings.Join(problems, " "), "逃逸") {
				t.Fatalf("problems = %v, want escape diagnostic", problems)
			}
		})
	}
}

func TestCheckpointGuardValidateMissingTarget(t *testing.T) {
	root := guardTestTree(t, map[string]string{"file.txt": "hello"})
	g := CheckpointGuard{SnapshotsEnabled: true}

	status, problems := g.Validate(root, "does-not-exist.txt")
	if status != RestoreValidationFailed {
		t.Fatalf("status = %q, want %q (problems: %v)", status, RestoreValidationFailed, problems)
	}
	if len(problems) == 0 || !strings.Contains(strings.Join(problems, " "), "不存在") {
		t.Fatalf("problems = %v, want missing-target diagnostic", problems)
	}
}

func TestCheckpointGuardValidateCJKPath(t *testing.T) {
	root := guardTestTree(t, map[string]string{
		"文档/子目录/报告 最终版.txt": "内容",
	})
	g := CheckpointGuard{SnapshotsEnabled: true}

	target := filepath.Join("文档", "子目录", "报告 最终版.txt")
	status, problems := g.Validate(root, target)
	if status != RestoreOK {
		t.Fatalf("status = %q, want %q (problems: %v)", status, RestoreOK, problems)
	}
	if len(problems) != 0 {
		t.Fatalf("problems = %v, want none", problems)
	}
}

func TestCheckpointGuardValidateReturnsProblemsCopy(t *testing.T) {
	root := guardTestTree(t, map[string]string{"file.txt": "hello"})
	g := CheckpointGuard{SnapshotsEnabled: true, Problems: []string{"预置问题"}}

	status, problems := g.Validate(root, "file.txt")
	if status != RestoreOK {
		t.Fatalf("status = %q, want %q", status, RestoreOK)
	}
	if len(problems) != 1 || problems[0] != "预置问题" {
		t.Fatalf("problems = %v, want [预置问题]", problems)
	}

	// 修改返回值不得影响 guard 内部状态。
	problems[0] = "已篡改"
	if g.Problems[0] != "预置问题" {
		t.Fatalf("internal Problems mutated: %v", g.Problems)
	}
	// 再次校验应得到一份全新的、未被污染的副本。
	_, again := g.Validate(root, "file.txt")
	if len(again) != 1 || again[0] != "预置问题" {
		t.Fatalf("problems on second call = %v, want [预置问题]", again)
	}
}

func TestCheckpointGuardValidateStatuses(t *testing.T) {
	root := guardTestTree(t, map[string]string{"file.txt": "hello"})

	t.Run("snapshots_disabled", func(t *testing.T) {
		g := CheckpointGuard{}
		status, _ := g.Validate(root, "file.txt")
		if status != RestoreSnapshotsDisabled {
			t.Fatalf("status = %q, want %q", status, RestoreSnapshotsDisabled)
		}
	})

	t.Run("unavailable", func(t *testing.T) {
		g := CheckpointGuard{
			SnapshotsEnabled: true,
			BaselineDir:      filepath.Join(root, "no-such-baseline"),
		}
		status, problems := g.Validate(root, "file.txt")
		if status != RestoreUnavailable {
			t.Fatalf("status = %q, want %q (problems: %v)", status, RestoreUnavailable, problems)
		}
	})

	t.Run("writable_baseline_ok", func(t *testing.T) {
		baseline := filepath.Join(root, "baseline")
		if err := os.MkdirAll(baseline, 0o755); err != nil {
			t.Fatal(err)
		}
		g := CheckpointGuard{SnapshotsEnabled: true, BaselineDir: baseline}
		status, problems := g.Validate(root, "file.txt")
		if status != RestoreOK {
			t.Fatalf("status = %q, want %q (problems: %v)", status, RestoreOK, problems)
		}
	})

	t.Run("not_a_git_repo", func(t *testing.T) {
		g := CheckpointGuard{SnapshotsEnabled: true, RequireGitRepo: true}
		status, _ := g.Validate(root, "file.txt")
		if status != RestoreNotAGitRepo {
			t.Fatalf("status = %q, want %q", status, RestoreNotAGitRepo)
		}
	})

	t.Run("git_repo_ok", func(t *testing.T) {
		if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		g := CheckpointGuard{SnapshotsEnabled: true, RequireGitRepo: true}
		status, problems := g.Validate(root, "file.txt")
		if status != RestoreOK {
			t.Fatalf("status = %q, want %q (problems: %v)", status, RestoreOK, problems)
		}
	})
}
