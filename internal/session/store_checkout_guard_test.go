package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// checkoutGuardFixture 建一个含三轮记录的会话，返回句柄与中间记录 id。
// 中间记录作为 checkout 目标，后续追加会挂到它之下。
func checkoutGuardFixture(t *testing.T, store *Store) (*Session, string) {
	t.Helper()
	sess, err := store.NewSession("守卫-会话")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(KindUser, "第一轮"); err != nil {
		t.Fatal(err)
	}
	middle, err := sess.AppendRecord(KindAssistant, "中间记录")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(KindUser, "最后一轮"); err != nil {
		t.Fatal(err)
	}
	return sess, middle.ID
}

// TestOpenSessionAtGuardOffBaseline 验证守卫关闭（默认）时 checkout 行为不变：
// 合法目标照常切换，且 store 上不残留守卫配置。
func TestOpenSessionAtGuardOffBaseline(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if store.guard != nil {
		t.Fatal("默认 store 不应带守卫")
	}
	sess, middleID := checkoutGuardFixture(t, store)

	reopened, err := store.OpenSessionAt(sess.ID(), middleID)
	if err != nil {
		t.Fatalf("守卫关闭时 OpenSessionAt: %v", err)
	}
	if leaf, ok := reopened.ActiveLeafID(); !ok || leaf != middleID {
		t.Fatalf("ActiveLeafID = %q, %v, want %q", leaf, ok, middleID)
	}
}

// TestOpenSessionAtGuardValidTargetPasses 验证守卫开启时合法目标仍然通过，
// 且 WithCheckpointGuard 返回副本而不改动原 store。
func TestOpenSessionAtGuardValidTargetPasses(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, middleID := checkoutGuardFixture(t, store)

	guarded := store.WithCheckpointGuard(NewCheckoutGuard())
	if store.guard != nil {
		t.Fatal("WithCheckpointGuard 改动了原 store")
	}
	reopened, err := guarded.OpenSessionAt(sess.ID(), middleID)
	if err != nil {
		t.Fatalf("守卫开启时合法目标被拒绝: %v", err)
	}
	if leaf, ok := reopened.ActiveLeafID(); !ok || leaf != middleID {
		t.Fatalf("ActiveLeafID = %q, %v, want %q", leaf, ok, middleID)
	}
}

// TestOpenSessionAtGuardRejectsNonRestoredStatus 验证非 restored 状态（此处用
// snapshots_disabled 触发）会在切换活动指针之前拒绝，且不返回可用会话句柄。
func TestOpenSessionAtGuardRejectsNonRestoredStatus(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, middleID := checkoutGuardFixture(t, store)

	guarded := store.WithCheckpointGuard(&CheckpointGuard{})
	reopened, err := guarded.OpenSessionAt(sess.ID(), middleID)
	if err == nil {
		t.Fatal("非 restored 状态的守卫竟放行了 checkout")
	}
	if reopened != nil {
		t.Fatalf("拒绝时仍返回会话句柄: %+v", reopened)
	}
	if !strings.Contains(err.Error(), string(RestoreSnapshotsDisabled)) {
		t.Fatalf("错误未带状态枚举 = %q", err)
	}
}

// TestValidateCheckoutTargetStatuses 穷举 RestoreStatus 枚举：仅 restored 放行，
// 其余状态一律拒绝且错误文本带对应状态字符串。
func TestValidateCheckoutTargetStatuses(t *testing.T) {
	root := guardTestTree(t, map[string]string{
		"文档/报告 最终版.jsonl": "{}",
	})
	cases := []struct {
		name   string
		guard  *CheckpointGuard
		target string
		want   RestoreStatus
	}{
		{
			name:   "restored",
			guard:  NewCheckoutGuard(),
			target: filepath.Join("文档", "报告 最终版.jsonl"),
			want:   RestoreOK,
		},
		{
			name:   "snapshots_disabled",
			guard:  &CheckpointGuard{},
			target: filepath.Join("文档", "报告 最终版.jsonl"),
			want:   RestoreSnapshotsDisabled,
		},
		{
			name:   "unavailable",
			guard:  &CheckpointGuard{SnapshotsEnabled: true, BaselineDir: filepath.Join(root, "缺失基线")},
			target: filepath.Join("文档", "报告 最终版.jsonl"),
			want:   RestoreUnavailable,
		},
		{
			name:   "not_a_git_repo",
			guard:  &CheckpointGuard{SnapshotsEnabled: true, RequireGitRepo: true},
			target: filepath.Join("文档", "报告 最终版.jsonl"),
			want:   RestoreNotAGitRepo,
		},
		{
			name:   "validation_failed",
			guard:  NewCheckoutGuard(),
			target: filepath.Join("文档", "不存在.jsonl"),
			want:   RestoreValidationFailed,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCheckoutTarget(tc.guard, root, tc.target)
			if tc.want == RestoreOK {
				if err != nil {
					t.Fatalf("restored 竟被拒绝: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("%s 竟被放行", tc.want)
			}
			if !strings.Contains(err.Error(), string(tc.want)) {
				t.Fatalf("错误 = %q, want 含 %q", err, tc.want)
			}
		})
	}
}

// TestValidateCheckoutTargetRejectsEscape 验证逃逸路径（相对 .. 与根目录之外的
// 绝对路径）都被拒绝，并给出 validation_failed 状态与问题描述。
func TestValidateCheckoutTargetRejectsEscape(t *testing.T) {
	root := guardTestTree(t, map[string]string{"inside.jsonl": "{}"})
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "outside.jsonl")
	if err := os.WriteFile(outside, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	guard := NewCheckoutGuard()
	for _, target := range []string{
		filepath.Join("..", "outside.jsonl"),
		outside,
	} {
		err := validateCheckoutTarget(guard, root, target)
		if err == nil {
			t.Fatalf("逃逸目标 %q 竟被放行", target)
		}
		if !strings.Contains(err.Error(), string(RestoreValidationFailed)) {
			t.Fatalf("逃逸目标 %q 错误 = %q, want %q", target, err, RestoreValidationFailed)
		}
	}
}

// TestValidateCheckoutTargetCJK 验证 CJK 路径按 UTF-8 原样校验，不被字节裁剪误伤。
func TestValidateCheckoutTargetCJK(t *testing.T) {
	root := guardTestTree(t, map[string]string{
		"文档/子目录/报告 最终版.jsonl": "内容",
	})
	target := filepath.Join("文档", "子目录", "报告 最终版.jsonl")
	if err := validateCheckoutTarget(NewCheckoutGuard(), root, target); err != nil {
		t.Fatalf("CJK 目标被拒绝: %v", err)
	}
}
