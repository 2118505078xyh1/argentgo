package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DecodeKind 表示一条物理 JSONL 行是否可信。
type DecodeKind int

const (
	DecodeOK DecodeKind = iota
	DecodeSyntax
	DecodeSchema
)

// ErrHardCorruption 标记无法安全修复的转录：任意位置出现 schema 错误，
// 或非最后一行出现语法错误。
var ErrHardCorruption = errors.New("session: hard corruption")

// 会话转录有大小上限，因此损坏或恶意文件无法在校验期间强制无界读取。
const maxRepairBytes = 64 << 20

// RepairReport 汇总对会话转录的一次校验。
type RepairReport struct {
	Path          string
	PhysicalLines int
	KeptLines     int
	TornTail      bool
	Repaired      bool
}

// ValidateAndRepair 在宽松的 readRecords 于历史被截断时重建会话树之前，检查 JSONL 转录。
// 只有最后一条物理行上的语法错误才是可恢复的残缺尾部；设置 repair 时，
// 会通过原子发布有效前缀来丢弃它。任意位置的 schema 错误，或非最后一条物理行上的语法错误，
// 都属于严重损坏，文件保持不动。仅仅缺少末尾换行符的有效文件，会通过一次单独的小修复补上。
func ValidateAndRepair(path string, repair bool) (RepairReport, error) {
	report := RepairReport{Path: path}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return report, nil
		}
		return report, fmt.Errorf("session: inspect %s: %w", path, err)
	}
	if info.Size() > maxRepairBytes {
		return report, fmt.Errorf("session: repair %s: file too large (%d bytes)", path, info.Size())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return report, fmt.Errorf("session: read %s: %w", path, err)
	}
	if len(data) > maxRepairBytes {
		return report, fmt.Errorf("session: repair %s: file too large (%d bytes)", path, len(data))
	}

	lines := strings.Split(string(data), "\n")
	// 恰好丢弃一个末尾空字符串，使最后一条物理行就是最后一条真实行，
	// 而额外的空行仍作为残缺尾部可见。
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	report.PhysicalLines = len(lines)
	last := len(lines) - 1

	// 空白行与 readRecords 的宽松解析保持一致：中间的空白行只是噪音，
	// 既不是语法错误也不代表历史丢失，必须跳过而不是把整份会话判为严重损坏。
	// 只有末尾连续的空白行才视作可修复的残缺尾部（见下面的 publishValidPrefix）。
	isBlank := func(line string) bool { return strings.TrimSpace(line) == "" }
	lastContent := -1
	for i, line := range lines {
		if !isBlank(line) {
			lastContent = i
		}
	}
	// publishValidPrefix 发布 idx 之前的行，并顺带裁掉末尾的空白行，
	// 使一次修复后文件保持干净（不会留下需要再次修复的尾部空行）。
	publishValidPrefix := func(idx int) error {
		valid := lines[:idx]
		for len(valid) > 0 && isBlank(valid[len(valid)-1]) {
			valid = valid[:len(valid)-1]
		}
		prefix := strings.Join(valid, "\n")
		if len(valid) > 0 {
			prefix += "\n"
		}
		return atomicPublishJSONL(path, []byte(prefix))
	}

	kept := 0
	for idx, line := range lines {
		if isBlank(line) {
			if idx <= lastContent {
				// 后面还有真实记录：这是中间的空白行，安全跳过。
				continue
			}
			// 末尾空白行等价于一条未写完的记录：按残缺尾部处理。
			report.TornTail = true
			report.KeptLines = kept
			if !repair {
				return report, nil
			}
			if err := publishValidPrefix(idx); err != nil {
				return report, fmt.Errorf("session: repair torn tail in %s: %w", path, err)
			}
			report.Repaired = true
			return report, nil
		}
		kind, detail := decodeLine(line)
		switch kind {
		case DecodeOK:
			kept++
		case DecodeSchema:
			report.KeptLines = kept
			return report, fmt.Errorf("%w: %s: line %d: %s",
				ErrHardCorruption, path, idx+1, detail)
		case DecodeSyntax:
			if idx != last {
				report.KeptLines = kept
				return report, fmt.Errorf("%w: %s: line %d: %s",
					ErrHardCorruption, path, idx+1, detail)
			}
			report.TornTail = true
			report.KeptLines = kept
			if !repair {
				return report, nil
			}
			if err := publishValidPrefix(idx); err != nil {
				return report, fmt.Errorf("session: repair torn tail in %s: %w", path, err)
			}
			report.Repaired = true
			return report, nil
		}
	}
	report.KeptLines = kept

	if repair && len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		// 空文件没有未结束的记录需要修复。保持它为空可使修复后的转录保持干净，
		// 而不是引入一条会在下次校验时被报告为残缺尾部的空行。
		if err := appendRepairNewline(path); err != nil {
			return report, fmt.Errorf("session: repair tail in %s: %w", path, err)
		}
		report.Repaired = true
	}
	return report, nil
}

// decodeLine 对一条物理行进行分类，并解释拒绝原因。缺少 id、parent 和 ts 字段是合法的，
// 因为 readRecords 会合成 id，并让其他零值保持原样。
func decodeLine(line string) (DecodeKind, string) {
	trimmed := strings.TrimSpace(line)
	if !json.Valid([]byte(line)) {
		return DecodeSyntax, "invalid JSON syntax"
	}
	if trimmed == "" || trimmed[0] != '{' {
		return DecodeSchema, "record is not a JSON object"
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &obj); err != nil || obj == nil {
		return DecodeSchema, "record is not a JSON object"
	}
	kind, ok := jsonString(obj["kind"])
	if !ok {
		return DecodeSchema, "kind is missing"
	}
	if kind == "" {
		return DecodeSchema, "kind must be a non-empty string"
	}
	for _, field := range []string{"id", "parent"} {
		if raw, present := obj[field]; present {
			if _, ok := jsonString(raw); !ok {
				return DecodeSchema, field + " must be a string"
			}
		}
	}
	if raw, present := obj["ts"]; present {
		stamp, ok := jsonString(raw)
		if !ok {
			return DecodeSchema, "ts must be a string"
		}
		if _, err := time.Parse(time.RFC3339, stamp); err != nil {
			return DecodeSchema, "ts must be RFC3339"
		}
	}
	return DecodeOK, ""
}

// jsonString 拒绝 null 和非字符串标量，否则 encoding/json 会静默把它们接受为 Go 字符串。
func jsonString(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	text, ok := value.(string)
	return text, ok
}

// atomicPublishJSONL 在目标文件旁边暂存一份完整的转录，在重命名前同步它，
// 并在任何失败时清理暂存文件。在 Windows 上，os.Rename 会替换已有目标
// （MOVEFILE_REPLACE_EXISTING）。
func atomicPublishJSONL(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".jsonl-*.tmp")
	if err != nil {
		return fmt.Errorf("stage temp file: %w", err)
	}
	name := tmp.Name()
	published := false
	defer func() {
		if !published {
			_ = tmp.Close()
			_ = os.Remove(name)
		}
	}()
	if info, err := os.Stat(path); err == nil {
		_ = tmp.Chmod(info.Mode().Perm())
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("stage repair: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync staged repair: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close staged repair: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("publish repair: %w", err)
	}
	published = true
	syncRepairDir(dir)
	return nil
}

// syncRepairDir 是尽力而为的：Windows 对目录句柄返回 EINVAL，某些文件系统直接拒绝 Sync，
// 这两种情况都不会撤销重命名。
func syncRepairDir(dir string) {
	handle, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = handle.Sync()
	_ = handle.Close()
}

// RecoverProgress 返回用于崩溃恢复的最新持久 assistant 进度检查点，
// 但会先拒绝落后于会话最后一条已提交 assistant 消息的检查点。
//
// 两道防线防止恢复把对话倒退：
//   - 位置：在检查点处或之后记录的 assistant 消息意味着该轮次已经完成，
//     其中途进度已过时。
//   - 轮次：Turn 不大于活动分支上已提交 assistant 消息数量的检查点，
//     属于已经持久化的轮次。来自当前进行中轮次的进度可以通过。
//
// 布尔值报告是否找到有效检查点。这是建议性的：
// 由 agent 决定是恢复、重放还是丢弃返回的文本和工具调用。ValidateAndRepair 本身不变。
func RecoverProgress(s *Session) (Progress, bool) {
	records, err := s.Records()
	if err != nil {
		return Progress{}, false
	}
	scope := progressScope(s, records)
	progress, progressIdx, ok := latestProgressRecord(scope)
	if !ok {
		return Progress{}, false
	}
	committed := 0
	for i, record := range scope {
		if record.Kind != KindAssistant {
			continue
		}
		if i >= progressIdx {
			// 一条已提交的 assistant 消息位于此检查点之后：它所属的轮次已完成，
			// 因此不要从中途状态恢复。
			return Progress{}, false
		}
		committed++
	}
	if progress.Turn <= committed {
		return Progress{}, false
	}
	return progress, true
}

// appendRepairNewline 修复未结束的最后一条记录，而不重写转录的其余部分。
// 它与残缺尾部修复分开，因此 schema 错误永远不会被一次表面性的重写所掩盖。
func appendRepairNewline(path string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	_, writeErr := file.Write([]byte("\n"))
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil {
		return fmt.Errorf("append newline: %w", writeErr)
	}
	if syncErr != nil {
		return fmt.Errorf("sync newline: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close: %w", closeErr)
	}
	return nil
}
