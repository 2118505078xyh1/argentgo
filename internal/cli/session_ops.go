package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/session"
)

// sessionsUndo 恢复最新检查点清单中列出的文件。
func sessionsUndo(app App, id string, args []string) int {
	workspace, err := undoWorkspace(args)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "argentgo:", err)
		return 2
	}
	sess, err := session.OpenSession(app.sessionDir(), id)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	checkpoint, record, err := sess.LatestCheckpoint()
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	restored, err := session.UndoCheckpoint(workspace, checkpoint)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	fmt.Fprintf(app.IO.Out, "restored %d file(s) from checkpoint %s in %s\n", len(restored), record.ID, sess.ID())
	return 0
}

func undoWorkspace(args []string) (string, error) {
	root := ""
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "-C" || args[i] == "--cwd":
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s needs a directory", args[i])
			}
			i++
			root = args[i]
		case strings.HasPrefix(args[i], "--cwd="):
			root = strings.TrimPrefix(args[i], "--cwd=")
		default:
			return "", fmt.Errorf("unexpected undo argument %q", args[i])
		}
	}
	if root == "" {
		return os.Getwd()
	}
	return filepath.Abs(root)
}

// sessionsRetry 在检查点的父节点处分叉会话并打印新的 id。
func sessionsRetry(app App, id string, args []string) int {
	checkpointID := ""
	if len(args) > 0 {
		checkpointID = args[0]
	}
	if len(args) > 1 {
		fmt.Fprintf(app.IO.Err, "argentgo: unexpected retry argument %q\n", args[1])
		return 2
	}
	fork, err := session.Retry(app.sessionDir(), id, checkpointID)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	fmt.Fprintln(app.IO.Out, fork.ID())
	return 0
}

// sessionsDiscardAttempt 把一次失败尝试分叉为新会话。stdout 只输出新会话 id，
// 便于调用方直接把它们传给 --session；续用提示走 stderr。
func sessionsDiscardAttempt(app App, id string, args []string) int {
	attemptID := ""
	if len(args) > 0 {
		attemptID = args[0]
	}
	if len(args) > 1 {
		fmt.Fprintf(app.IO.Err, "argentgo: unexpected discard-attempt argument %q\n", args[1])
		return 2
	}

	// OpenSession 会对转录调用 ValidateAndRepair(path, true)：缺少末尾换行的健康
	// 文件会被补一个换行，残缺尾部会被裁掉。这里先把源文件逐字节复制到临时目录，
	// 在副本上打开并分叉，保证源转录永远不被写入。
	tmp, err := os.MkdirTemp("", "argent-discard-attempt-*")
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	defer os.RemoveAll(tmp)

	sess, err := openSessionReadOnlyCopy(app.sessionDir(), id, tmp)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	if attemptID == "" {
		attemptID, err = defaultDiscardAttemptID(sess)
		if err != nil {
			fmt.Fprintf(app.IO.Err, "argentgo: %v\n", err)
			return 1
		}
	}
	fork, err := sess.DiscardFailedAttempt(attemptID)
	if err != nil {
		if hint := discardAttemptHint(err); hint != "" {
			fmt.Fprintf(app.IO.Err, "argentgo: %s\n", hint)
			return 1
		}
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	// Fork 把新会话写在临时目录中；发布到真实会话目录后，返回的 id 才可立即使用。
	if err := publishDiscardFork(app.sessionDir(), tmp, fork.ID()); err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	fmt.Fprintln(app.IO.Out, fork.ID())
	fmt.Fprintf(app.IO.Err, "提示：使用 --session %s --resume 从新会话继续\n", fork.ID())
	return 0
}

// openSessionReadOnlyCopy 把源转录原样复制到 tmp 并在副本上打开会话。
// 先用 ValidateAndRepair(path, false) 做只读校验：硬损坏在此直接拒绝，不触碰源文件。
func openSessionReadOnlyCopy(dir, id, tmp string) (*session.Session, error) {
	path, err := session.ResolvePath(dir, id)
	if err != nil {
		return nil, err
	}
	if _, err := session.ValidateAndRepair(path, false); err != nil {
		return nil, err
	}
	source, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	copyPath := filepath.Join(tmp, filepath.Base(path))
	if err := os.WriteFile(copyPath, source, 0o644); err != nil {
		return nil, err
	}
	return session.OpenSession(tmp, id)
}

// defaultDiscardAttemptID 选取活动分支上最后一个 reason=failed 的尝试记录 id。
func defaultDiscardAttemptID(sess *session.Session) (string, error) {
	attempts, err := sess.Attempts()
	if err != nil {
		return "", err
	}
	failed := -1
	for i, attempt := range attempts {
		if attempt.Reason == session.AttemptFailed {
			failed = i
		}
	}
	if failed < 0 {
		return "", errors.New("活动分支上没有 reason=failed 的尝试，请用 sessions discard-attempt <id> <attempt-id> 显式指定")
	}
	ids, err := activeAttemptIDs(sess)
	if err != nil {
		return "", err
	}
	if failed >= len(ids) {
		return "", fmt.Errorf("无法定位失败尝试的记录 id（序号 %d）", failed)
	}
	return ids[failed], nil
}

// activeAttemptIDs 按活动分支顺序返回可解析的 KindAttempt 记录 id。
// 活动叶的判定与 session 包一致：文件顺序最后一条记录。
func activeAttemptIDs(sess *session.Session) ([]string, error) {
	records, err := sess.Records()
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, nil
	}
	byID := make(map[string]session.Record, len(records))
	for _, record := range records {
		byID[record.ID] = record
	}
	var ids []string
	seen := make(map[string]bool, len(records))
	for leaf := records[len(records)-1].ID; leaf != ""; {
		record, ok := byID[leaf]
		if !ok || seen[leaf] {
			break
		}
		seen[leaf] = true
		if record.Kind == session.KindAttempt {
			var attempt session.Attempt
			if json.Unmarshal(record.Data, &attempt) == nil {
				ids = append(ids, record.ID)
			}
		}
		leaf = record.Parent
	}
	for i, j := 0, len(ids)-1; i < j; i, j = i+1, j-1 {
		ids[i], ids[j] = ids[j], ids[i]
	}
	return ids, nil
}

// discardAttemptHint 把 fail-closed 的哨兵错误转成稳定的中文提示，
// 其余错误返回空串，由调用方按普通错误报告。
func discardAttemptHint(err error) string {
	switch {
	case errors.Is(err, session.ErrAttemptNotDiscardable):
		return "该尝试含成功结果或缺少失败证据，不满足安全丢弃条件，已拒绝"
	case errors.Is(err, session.ErrAttemptBranchOffPath):
		return "该尝试的分叉点不在当前活动路径上，已拒绝跨分支丢弃"
	default:
		return ""
	}
}

// publishDiscardFork 把临时目录中生成的新会话发布到真实会话目录。
func publishDiscardFork(dir, tmp, forkID string) error {
	if !safeSessionID(forkID) {
		return fmt.Errorf("invalid discard fork id %q", forkID)
	}
	target := filepath.Join(dir, forkID+".jsonl")
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("discard fork id %q already exists", forkID)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := os.ReadFile(filepath.Join(tmp, forkID+".jsonl"))
	if err != nil {
		return err
	}
	return writeFileNoFollow(target, data, 0o644)
}

// sessionsName 存储显式的标题覆盖值。
func sessionsName(app App, id, title string) int {
	sess, err := session.OpenSession(app.sessionDir(), id)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	if err := ApplyTitleToSession(sess, title); err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	fmt.Fprintf(app.IO.Out, "renamed session %s\n", sess.ID())
	return 0
}
