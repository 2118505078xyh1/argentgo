package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/session"
)

// sessionsUndo 恢复最近 steps 个检查点清单中列出的文件。
// steps<=1（含未指定 --steps）保留原有单检查点路径，行为与输出不变。
func sessionsUndo(app App, id string, args []string) int {
	baseArgs, steps, err := splitUndoSteps(args)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "argentgo:", err)
		return 2
	}
	workspace, err := undoWorkspace(baseArgs)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "argentgo:", err)
		return 2
	}
	sess, err := session.OpenSession(app.sessionDir(), id)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	if steps <= 1 {
		checkpoint, record, err := sess.LatestCheckpoint()
		if err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		// undo 先过策略 → 审批 → 审计链；拒绝时不触碰工作区。
		if err := checkUndoRestore(app, workspace, sess.ID(), checkpoint); err != nil {
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
	// 多步撤销：先收集窗口内的全部检查点并校验它们都存在，
	// 再合成一份清单用一次 UndoCheckpoint 原子还原，避免逐次撤销在中途冲突时
	// 留下半还原状态。
	checkpoints, record, err := undoCheckpointWindow(sess, steps)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	merged := mergeUndoCheckpoints(checkpoints)
	// 多步撤销同样先过安全链，清单是合成后的最终还原目标。
	if err := checkUndoRestore(app, workspace, sess.ID(), merged); err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	restored, err := session.UndoCheckpoint(workspace, merged)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	fmt.Fprintf(app.IO.Out, "restored %d file(s) from checkpoint %s in %s\n", len(restored), record.ID, sess.ID())
	return 0
}

// splitUndoSteps 从撤销参数中剥离 --steps（支持 --steps N 与 --steps=N），
// 返回剩余参数与要回退的检查点数；未指定时步数为 1，剩余参数保持原样。
func splitUndoSteps(args []string) ([]string, int, error) {
	steps := 1
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--steps":
			if i+1 >= len(args) {
				return nil, 0, errors.New("--steps 需要一个正整数")
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 {
				return nil, 0, fmt.Errorf("--steps 值 %q 非法，需要正整数", args[i])
			}
			steps = n
		case strings.HasPrefix(args[i], "--steps="):
			raw := strings.TrimPrefix(args[i], "--steps=")
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 {
				return nil, 0, fmt.Errorf("--steps 值 %q 非法，需要正整数", raw)
			}
			steps = n
		default:
			rest = append(rest, args[i])
		}
	}
	return rest, steps, nil
}

// undoCheckpointWindow 沿活动分支收集最近 steps 个检查点（由近及远），
// 并返回最远那个检查点的记录，供输出标识还原到的目标。
func undoCheckpointWindow(sess *session.Session, steps int) ([]session.Checkpoint, session.Record, error) {
	checkpoints := make([]session.Checkpoint, 0, steps)
	var farthest session.Record
	for i := 1; i <= steps; i++ {
		cp, record, err := sess.CheckpointBack(i)
		if err != nil {
			return nil, session.Record{}, err
		}
		checkpoints = append(checkpoints, cp)
		farthest = record
	}
	return checkpoints, farthest, nil
}

// mergeUndoCheckpoints 把多个检查点合成一份撤销清单。
// 由近及远遍历时，某路径第一次出现来自窗口内最近的检查点，其轮次后状态即当前
// 工作区状态，用作校验基准；最后一次覆盖来自窗口内最远的检查点，其轮次前状态
// 即撤销多个轮次后应到达的目标。两者拼接后可由一次 UndoCheckpoint 完成原子还原。
func mergeUndoCheckpoints(checkpoints []session.Checkpoint) session.Checkpoint {
	type snapshots struct {
		before session.CheckpointFile
		after  session.CheckpointFile
	}
	byPath := make(map[string]*snapshots)
	var order []string
	for _, cp := range checkpoints {
		for _, file := range cp.Files {
			snap, ok := byPath[file.Path]
			if !ok {
				snap = &snapshots{after: file}
				byPath[file.Path] = snap
				order = append(order, file.Path)
			}
			snap.before = file
		}
	}
	merged := session.Checkpoint{Files: make([]session.CheckpointFile, 0, len(order))}
	for _, path := range order {
		snap := byPath[path]
		merged.Files = append(merged.Files, session.CheckpointFile{
			Path:        path,
			SHA256:      snap.before.SHA256,
			Before:      snap.before.Before,
			Absent:      snap.before.Absent,
			Mode:        snap.before.Mode,
			AfterSHA256: snap.after.AfterSHA256,
			AfterAbsent: snap.after.AfterAbsent,
		})
	}
	return merged
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
// --steps N（N>1）时取活动分支上倒数第 N 个检查点的记录 id 作为分叉点。
func sessionsRetry(app App, id string, args []string) int {
	positional, steps, err := splitUndoSteps(args)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "argentgo:", err)
		return 2
	}
	checkpointID := ""
	if len(positional) > 0 {
		checkpointID = positional[0]
	}
	if len(positional) > 1 {
		fmt.Fprintf(app.IO.Err, "argentgo: unexpected retry argument %q\n", positional[1])
		return 2
	}
	if steps > 1 {
		if checkpointID != "" {
			fmt.Fprintln(app.IO.Err, "argentgo: --steps 不能与显式检查点 id 同时使用")
			return 2
		}
		sess, err := session.OpenSession(app.sessionDir(), id)
		if err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		_, record, err := sess.CheckpointBack(steps)
		if err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		checkpointID = record.ID
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
