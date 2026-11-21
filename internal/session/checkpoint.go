package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ErrCheckpointConflict 表示文件在检查点完成之后发生了更改。
// Undo 拒绝覆盖这样的手动编辑。
var ErrCheckpointConflict = errors.New("session: checkpoint conflict")

// ErrNoCheckpoint 表示会话没有检查点记录。
var ErrNoCheckpoint = errors.New("session: no checkpoint in session")

// CheckpointFile 保存一个轮次前的快照。Path 始终是相对于工作区、以斜杠分隔的路径；
// SHA256 是轮次前字节的哈希，Before 携带这些字节，使撤销可以自包含。
// Absent 表示该轮次之前文件不存在；After 系列字段描述轮次结束时（包括中止的轮次）记录的状态。
type CheckpointFile struct {
	Path        string `json:"path"`
	SHA256      string `json:"sha256"`
	Before      []byte `json:"before,omitempty"`
	Absent      bool   `json:"absent,omitempty"`
	Mode        uint32 `json:"mode,omitempty"`
	AfterSHA256 string `json:"afterSha256,omitempty"`
	AfterAbsent bool   `json:"afterAbsent,omitempty"`
}

// Checkpoint 是 kind:"checkpoint" 记录的载荷。Parent 是执行变更轮次之前活动叶节点的记录 id，
// 这样 /retry 可以恰好从该点分叉。
type Checkpoint struct {
	Parent  string           `json:"parent,omitempty"`
	Prompt  string           `json:"prompt,omitempty"`
	Files   []CheckpointFile `json:"files"`
	Partial bool             `json:"partial,omitempty"`
	Created time.Time        `json:"created,omitempty"`
}

// hashCacheKey 为检查点哈希缓存标识文件的一个版本。它把解析后的目标路径
// （其中编码了工作区和相对路径）与 CaptureCheckpoint 捕获的 stat 元数据组合在一起。
// 只有当大小、mtime 和模式都保持不变时，才会复用缓存条目。
type hashCacheKey struct {
	target string
	size   int64
	mtime  int64
	mode   os.FileMode
}

// hashCacheEntry 将轮次前的字节及其哈希保存在一起，因为检查点把字节存放在 Before 中，
// 使撤销保持自包含。因此要跳过读取，就必须同时缓存字节和摘要。
type hashCacheEntry struct {
	sha256 string
	before []byte
}

var checkpointHashCache = struct {
	sync.Mutex
	entries map[hashCacheKey]hashCacheEntry
}{entries: make(map[hashCacheKey]hashCacheEntry)}

// lookupCheckpointHash 返回 key 对应缓存条目的副本。复制字节可防止调用方修改
// CheckpointFile.Before 时破坏缓存。
func lookupCheckpointHash(key hashCacheKey) (hashCacheEntry, bool) {
	checkpointHashCache.Lock()
	defer checkpointHashCache.Unlock()
	entry, ok := checkpointHashCache.entries[key]
	if ok {
		entry.before = append([]byte(nil), entry.before...)
	}
	return entry, ok
}

// rememberCheckpointHash 缓存新读取文件的哈希和字节。
func rememberCheckpointHash(key hashCacheKey, sha256 string, before []byte) {
	entry := hashCacheEntry{sha256: sha256, before: append([]byte(nil), before...)}
	checkpointHashCache.Lock()
	checkpointHashCache.entries[key] = entry
	checkpointHashCache.Unlock()
}

// ClearCheckpointHashCache 丢弃所有缓存的检查点哈希和字节切片。
// 缓存只是纯粹的加速手段：清除它绝不会改变检查结果，
// 只会让下一次 CaptureCheckpoint 重新读取并重新计算其文件的哈希。
func ClearCheckpointHashCache() {
	checkpointHashCache.Lock()
	checkpointHashCache.entries = make(map[hashCacheKey]hashCacheEntry)
	checkpointHashCache.Unlock()
}

// InvalidateCheckpointHashCache 丢弃 workspace 下 rel 的所有缓存版本。
// 当已知文件在检查点轮次之外发生了更改，而仅基于 stat 元数据的键无法察觉时
// （例如大小不变且恢复了 mtime 的编辑），应调用此函数。未知或无法解析的路径为空操作。
func InvalidateCheckpointHashCache(workspace, rel string) {
	target, err := resolveWorkspacePath(workspace, rel)
	if err != nil {
		return
	}
	checkpointHashCache.Lock()
	for key := range checkpointHashCache.entries {
		if key.target == target {
			delete(checkpointHashCache.entries, key)
		}
	}
	checkpointHashCache.Unlock()
}

// checkpointFileReader 是 CaptureCheckpoint 使用的可注入文件读取器。
// 生产调用方使用 os.ReadFile；测试则替换为计数读取器。
type checkpointFileReader func(string) ([]byte, error)

// CaptureCheckpoint 对 paths 的轮次前字节进行快照，这些路径必须相对于 workspace。
// 路径会去重并排序，以生成稳定的清单。未更改的文件由进程内哈希缓存提供；
// 该缓存严格来说只是优化，正确性从不依赖它。
func CaptureCheckpoint(workspace string, paths []string) (Checkpoint, error) {
	return captureCheckpoint(workspace, paths, os.ReadFile)
}

func captureCheckpoint(workspace string, paths []string, read checkpointFileReader) (Checkpoint, error) {
	cp := Checkpoint{Files: make([]CheckpointFile, 0, len(paths)), Created: time.Now().UTC()}
	seen := make(map[string]bool, len(paths))
	for _, raw := range paths {
		rel, err := normalizeRelativePath(raw)
		if err != nil {
			return Checkpoint{}, err
		}
		if seen[rel] {
			continue
		}
		seen[rel] = true
		target, err := resolveWorkspacePath(workspace, rel)
		if err != nil {
			return Checkpoint{}, err
		}
		info, err := os.Lstat(target)
		file := CheckpointFile{Path: rel}
		if errors.Is(err, os.ErrNotExist) {
			file.Absent = true
		} else if err != nil {
			return Checkpoint{}, fmt.Errorf("session: stat %s: %w", rel, err)
		} else {
			if !info.Mode().IsRegular() {
				return Checkpoint{}, fmt.Errorf("session: checkpoint path %q is not a regular file", rel)
			}
			key := hashCacheKey{
				target: target,
				size:   info.Size(),
				mtime:  info.ModTime().UnixNano(),
				mode:   info.Mode(),
			}
			var bytes []byte
			if entry, ok := lookupCheckpointHash(key); ok {
				bytes = entry.before
				file.SHA256 = entry.sha256
			} else {
				bytes, err = read(target)
				if err != nil {
					return Checkpoint{}, fmt.Errorf("session: read %s: %w", rel, err)
				}
				file.SHA256 = hashBytes(bytes)
				rememberCheckpointHash(key, file.SHA256, bytes)
			}
			file.Before = bytes
			file.Mode = uint32(info.Mode().Perm())
		}
		cp.Files = append(cp.Files, file)
	}
	sort.Slice(cp.Files, func(i, j int) bool { return cp.Files[i].Path < cp.Files[j].Path })
	return cp, nil
}

// CompleteCheckpoint 记录清单中每一项的轮次后状态。即使轮次被取消，也必须运行它，
// 并在清单上设置 Partial。
func CompleteCheckpoint(workspace string, cp *Checkpoint) error {
	if cp == nil {
		return errors.New("session: nil checkpoint")
	}
	for i := range cp.Files {
		file := &cp.Files[i]
		target, err := resolveWorkspacePath(workspace, file.Path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(target)
		if errors.Is(err, os.ErrNotExist) {
			file.AfterAbsent = true
			file.AfterSHA256 = ""
			continue
		}
		if err != nil {
			return fmt.Errorf("session: stat %s: %w", file.Path, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("session: checkpoint path %q became a non-regular file", file.Path)
		}
		bytes, err := os.ReadFile(target)
		if err != nil {
			return fmt.Errorf("session: read %s: %w", file.Path, err)
		}
		file.AfterAbsent = false
		file.AfterSHA256 = hashBytes(bytes)
	}
	return nil
}

// RecordCheckpoint 向 s 追加一个检查点并返回其存储记录。
// 在执行变更的工具批次之前，先调用 CaptureCheckpoint 和 RecordCheckpoint。
func (s *Session) RecordCheckpoint(cp Checkpoint) (Record, error) {
	records, err := s.Records()
	if err != nil {
		return Record{}, err
	}
	if path := s.activePath(records); len(path) > 0 {
		cp.Parent = path[len(path)-1].ID
	}
	cp.Created = time.Now().UTC()
	return s.AppendRecord(KindCheckpoint, cp)
}

// LatestCheckpoint 返回活动分支上最后一个检查点及其记录。
// 当会话没有检查点时返回 ErrNoCheckpoint。
func (s *Session) LatestCheckpoint() (Checkpoint, Record, error) {
	return s.CheckpointBack(1)
}

// CheckpointBack 返回活动分支上倒数第 N 个检查点（从叶节点往回数，1 表示最近的一个）及其记录。
// 它支撑"撤销最近 N 个轮次"和"从 N 个轮次前重试"，而无需向前端暴露私有的路径遍历器。
func (s *Session) CheckpointBack(steps int) (Checkpoint, Record, error) {
	if steps < 1 {
		return Checkpoint{}, Record{}, fmt.Errorf("session: checkpoint steps must be >= 1, got %d", steps)
	}
	records, err := s.Records()
	if err != nil {
		return Checkpoint{}, Record{}, err
	}
	path := s.activePath(records)
	found := 0
	for i := len(path) - 1; i >= 0; i-- {
		if path[i].Kind != KindCheckpoint {
			continue
		}
		var cp Checkpoint
		if err := jsonUnmarshal(path[i].Data, &cp); err != nil {
			continue
		}
		found++
		if found == steps {
			return cp, path[i], nil
		}
	}
	return Checkpoint{}, Record{}, ErrNoCheckpoint
}

// UndoCheckpoint 只还原 cp 中指定的文件。如果某个文件当前的字节既不匹配记录的轮次后状态，
// 也不匹配轮次前状态，则视为冲突的手动编辑，并返回 ErrCheckpointConflict 拒绝操作。
//
// argent-go 的检查点不基于 git：没有可重置的 HEAD，也没有可比较的 update-ref。
// "拒绝破坏历史"的保证改为按文件表达。在任何写入之前，清单中的每一项都必须
// 仍处于检查点完成时记录的轮次后状态。如果任何文件已经前进（手动编辑或后续轮次），
// 则整体拒绝撤销，而不是静默覆盖那些工作。每次写入都会在修改文件前立即重新验证预期状态
// ——相当于 `git update-ref HEAD new old`——从而关闭针对并发写入者的检查时/使用时
// （time-of-check/time-of-use）窗口。如果后续文件未通过该复查，本次已还原的文件会回滚，
// 因此失败的撤销绝不会让工作区处于半还原状态。
func UndoCheckpoint(workspace string, cp Checkpoint) ([]string, error) {
	return undoCheckpoint(workspace, cp, nil)
}

// undoAction 是一次经过校验的文件变更。preUndo/preUndoMode 记录校验所接受的状态，
// 这样后续失败时可以回滚较早的写入，使失败的撤销保持全有或全无。
type undoAction struct {
	target      string
	file        CheckpointFile
	remove      bool
	preUndo     []byte
	preUndoMode os.FileMode
}

// undoCheckpoint 实现 UndoCheckpoint。beforeWrite 非空时，会在每次修改文件前立即运行；
// 测试使用它模拟并发编辑，并断言 CAS 复查会拒绝该编辑。生产调用方只能通过
// 传入 nil 钩子的 UndoCheckpoint 到达这里。
func undoCheckpoint(workspace string, cp Checkpoint, beforeWrite func(string)) ([]string, error) {
	var actions []undoAction
	for _, file := range cp.Files {
		rel, err := normalizeRelativePath(file.Path)
		if err != nil {
			return nil, err
		}
		target, err := resolveWorkspacePath(workspace, rel)
		if err != nil {
			return nil, err
		}
		info, statErr := os.Lstat(target)
		exists := statErr == nil
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return nil, fmt.Errorf("session: stat %s: %w", rel, statErr)
		}
		if exists && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
			return nil, fmt.Errorf("%w: %s is not a regular file", ErrCheckpointConflict, rel)
		}
		current := ""
		var currentBytes []byte
		if exists {
			currentBytes, err = os.ReadFile(target)
			if err != nil {
				return nil, fmt.Errorf("session: read %s: %w", rel, err)
			}
			current = hashBytes(currentBytes)
		}
		item := undoAction{target: target, file: file, preUndo: currentBytes}
		if exists {
			item.preUndoMode = info.Mode().Perm()
		}
		if file.Absent {
			if !exists {
				continue
			}
			if file.AfterSHA256 == "" || current != file.AfterSHA256 {
				return nil, fmt.Errorf("%w: %s changed after the checkpoint", ErrCheckpointConflict, rel)
			}
			item.remove = true
			actions = append(actions, item)
			continue
		}
		if !exists {
			if !file.AfterAbsent {
				return nil, fmt.Errorf("%w: %s was deleted after the checkpoint", ErrCheckpointConflict, rel)
			}
		} else if current != file.SHA256 {
			if file.AfterSHA256 == "" || current != file.AfterSHA256 {
				return nil, fmt.Errorf("%w: %s changed after the checkpoint", ErrCheckpointConflict, rel)
			}
		} else {
			continue // 已处于轮次前状态
		}
		actions = append(actions, item)
	}

	restored := make([]string, 0, len(actions))
	applied := make([]undoAction, 0, len(actions))
	abort := func(cause error) ([]string, error) {
		if err := rollbackUndoCheckpoint(applied); err != nil {
			return nil, fmt.Errorf("%w; rollback failed: %v", cause, err)
		}
		return nil, cause
	}
	for _, item := range actions {
		if beforeWrite != nil {
			beforeWrite(item.target)
		}
		// CAS：重新验证文件仍处于清单记录的轮次后状态（即"旧"值）。
		// 在校验通过之后到达的并发编辑不得被覆盖。
		if err := verifyRestoreTarget(item.target, item.file); err != nil {
			return abort(err)
		}
		if item.remove {
			if err := os.Remove(item.target); err != nil && !errors.Is(err, os.ErrNotExist) {
				return abort(fmt.Errorf("session: remove %s: %w", item.file.Path, err))
			}
			applied = append(applied, item)
			restored = append(restored, item.file.Path)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(item.target), 0o755); err != nil {
			return abort(fmt.Errorf("session: create parent of %s: %w", item.file.Path, err))
		}
		mode := os.FileMode(0o644)
		if item.file.Mode != 0 {
			mode = os.FileMode(item.file.Mode)
		}
		if err := os.WriteFile(item.target, item.file.Before, mode); err != nil {
			return abort(fmt.Errorf("session: restore %s: %w", item.file.Path, err))
		}
		if err := os.Chmod(item.target, mode); err != nil {
			return abort(fmt.Errorf("session: chmod %s: %w", item.file.Path, err))
		}
		applied = append(applied, item)
		restored = append(restored, item.file.Path)
	}
	return restored, nil
}

// rollbackUndoCheckpoint 将已经执行的操作还原到校验时看到的状态。它按逆序运行，且是尽力而为的：
// 二次清理失败会报告给调用方，但绝不会取代触发回滚的主冲突/写入错误。
func rollbackUndoCheckpoint(applied []undoAction) error {
	var errs []error
	for i := len(applied) - 1; i >= 0; i-- {
		item := applied[i]
		switch {
		case item.remove:
			// 撤销删除了该轮次创建的文件；把它放回去。
			mode := item.preUndoMode
			if mode == 0 {
				mode = 0o644
			}
			if err := os.MkdirAll(filepath.Dir(item.target), 0o755); err != nil {
				errs = append(errs, err)
				continue
			}
			if err := os.WriteFile(item.target, item.preUndo, mode); err != nil {
				errs = append(errs, err)
				continue
			}
			if err := os.Chmod(item.target, mode); err != nil {
				errs = append(errs, err)
			}
		case item.file.AfterAbsent:
			// 撤销重新创建了该轮次删除的文件；再次将其删除。
			if err := os.Remove(item.target); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, err)
			}
		default:
			// 撤销覆盖了该文件的字节；把轮次后的字节写回。
			mode := item.preUndoMode
			if mode == 0 {
				mode = 0o644
			}
			if err := os.WriteFile(item.target, item.preUndo, mode); err != nil {
				errs = append(errs, err)
				continue
			}
			if err := os.Chmod(item.target, mode); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// verifyRestoreTarget 重新检查 target 是否仍处于 file 中记录的轮次后状态，
// 即概念上的比较并交换（compare-and-swap）中的"旧"值。它镜像 undoCheckpoint 中的首轮校验，
// 并在每次修改前立即运行，使并发编辑不会被静默覆盖。
func verifyRestoreTarget(target string, file CheckpointFile) error {
	info, err := os.Lstat(target)
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("session: stat %s: %w", file.Path, err)
	}
	current := ""
	if exists {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s is not a regular file", ErrCheckpointConflict, file.Path)
		}
		bytes, err := os.ReadFile(target)
		if err != nil {
			return fmt.Errorf("session: read %s: %w", file.Path, err)
		}
		current = hashBytes(bytes)
	}
	changed := fmt.Errorf("%w: %s changed concurrently after the checkpoint", ErrCheckpointConflict, file.Path)
	switch {
	case file.Absent:
		// 该轮次创建了这个文件，而撤销会删除它；它必须仍是该轮次留下的那个文件。
		if !exists || file.AfterSHA256 == "" || current != file.AfterSHA256 {
			return changed
		}
	case file.AfterAbsent:
		// 该轮次删除了这个文件，而撤销会重新创建它；它必须仍然不存在。
		if exists {
			return changed
		}
	default:
		// 该轮次修改了这个文件，而撤销会还原轮次前的字节；它必须仍匹配检查点完成时记录的轮次后哈希。
		if !exists || file.AfterSHA256 == "" || current != file.AfterSHA256 {
			return changed
		}
	}
	return nil
}

// RetryFromCheckpoint 在检查点的父节点处分叉 s，并在分叉上记录一次重试尝试。
// 源会话文件永远不会被修改。空的 id 会选择活动分支上最新的检查点；
// 检查点记录 id 或其父记录 id 均可接受。
func (s *Session) RetryFromCheckpoint(id string) (*Session, error) {
	checkpoint, record, err := s.findCheckpoint(id)
	if err != nil {
		return nil, err
	}
	parent := checkpoint.Parent
	if parent == "" {
		path, err := s.Path(record.ID)
		if err != nil {
			return nil, err
		}
		if len(path) > 1 {
			parent = path[len(path)-2].ID
		} else if len(path) == 1 {
			parent = path[0].ID
		}
	}
	if parent == "" {
		return nil, ErrNoCheckpoint
	}
	fork, err := s.Fork(parent)
	if err != nil {
		return nil, err
	}
	// 分叉血缘在此落地：Attempt 的 SourceSession/SourceRecord 标明这次重试
	// 来自哪个会话的哪条检查点记录，供树视图与审计使用。它是 forkLineagePayload
	// 之外既有且唯一的血缘记录方式，后者刻意未与 Fork 接线（见 fork_lineage.go）。
	_, err = fork.AppendAttempt(Attempt{
		Reason:        AttemptRetried,
		Message:       "forked from checkpoint " + record.ID,
		SourceSession: s.ID(),
		SourceRecord:  record.ID,
		Prompt:        checkpoint.Prompt,
	})
	if err != nil {
		return nil, err
	}
	return fork, nil
}

// Retry 是 RetryFromCheckpoint 的 store 作用域版本。
func Retry(dir, id, checkpointID string) (*Session, error) {
	sess, err := OpenSession(dir, id)
	if err != nil {
		return nil, err
	}
	return sess.RetryFromCheckpoint(checkpointID)
}

func (s *Session) findCheckpoint(id string) (Checkpoint, Record, error) {
	records, err := s.Records()
	if err != nil {
		return Checkpoint{}, Record{}, err
	}
	if id == "" {
		return s.LatestCheckpoint()
	}
	candidates := records
	if path := s.activePath(records); len(path) > 0 {
		candidates = path
	}
	for _, record := range candidates {
		if record.Kind != KindCheckpoint {
			continue
		}
		var cp Checkpoint
		if jsonUnmarshal(record.Data, &cp) != nil {
			continue
		}
		if record.ID == id || cp.Parent == id {
			return cp, record, nil
		}
	}
	return Checkpoint{}, Record{}, fmt.Errorf("session: no checkpoint %q in %s", id, s.ID())
}

// normalizeRelativePath 将存储的路径转换为以斜杠分隔的相对路径。
// 在触碰任何文件之前，绝对路径和 .. 段都会被拒绝。
func normalizeRelativePath(raw string) (string, error) {
	rel := strings.TrimSpace(raw)
	if rel == "" || strings.ContainsRune(rel, 0) {
		return "", fmt.Errorf("session: empty checkpoint path")
	}
	if filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" ||
		strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, `\`) {
		return "", fmt.Errorf("session: absolute checkpoint path %q", raw)
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("session: checkpoint path escapes workspace: %q", raw)
	}
	return filepath.ToSlash(clean), nil
}

// resolveWorkspacePath 将规范化的相对路径映射为绝对路径，
// 拒绝符号链接组件，使检查点永远无法通过指向工作区之外的链接进行还原。
func resolveWorkspacePath(workspace, rel string) (string, error) {
	root := strings.TrimSpace(workspace)
	if root == "" {
		return "", errors.New("session: empty checkpoint workspace")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("session: resolve workspace: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	normalized, err := normalizeRelativePath(rel)
	if err != nil {
		return "", err
	}
	target := root
	for _, part := range strings.Split(filepath.FromSlash(normalized), string(os.PathSeparator)) {
		if part == "" || part == "." {
			continue
		}
		target = filepath.Join(target, part)
		info, err := os.Lstat(target)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("session: stat %s: %w", normalized, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("session: checkpoint path uses symlink: %q", normalized)
		}
	}
	relCheck, err := filepath.Rel(root, target)
	if err != nil || relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("session: checkpoint path escapes workspace: %q", normalized)
	}
	return target, nil
}

func hashBytes(bytes []byte) string {
	sum := sha256.Sum256(bytes)
	return hex.EncodeToString(sum[:])
}

func jsonUnmarshal(raw json.RawMessage, target any) error {
	if len(raw) == 0 {
		return errors.New("session: empty record payload")
	}
	return json.Unmarshal(raw, target)
}
