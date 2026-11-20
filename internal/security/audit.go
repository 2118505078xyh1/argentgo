package security

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// AuditEntry 是一条被审计的动作。字段名即 JSONL 的键。
type AuditEntry struct {
	// TS 是 RFC 3339 UTC 时间戳；为空时由 Record 填充。
	TS string `json:"ts"`
	// Tool 是被检查的工具名。
	Tool string `json:"tool"`
	// Decision 取 allow、ask、deny、refused、unavailable 或 error。
	// "unavailable" 表示没有操作员能回答，与 "refused"（明确的否定）相对。
	Decision string `json:"decision"`
	// Reason 是策略或审批的说明，已经脱敏。
	Reason string `json:"reason,omitempty"`
	// Args 保存脱敏后的工具参数。
	Args string `json:"args,omitempty"`
	// Session 标识产生该调用的会话。
	Session string `json:"session,omitempty"`
	// PrivacyMode 记录此决定生效的隐私姿态。
	// 空值保留隐私功能出现之前的行为（该键被完全省略）。
	PrivacyMode string `json:"privacy_mode,omitempty"`
}

// AuditLog 把脱敏后的决定按 UTC 每天一个 JSONL 文件追加到某个目录下。
// 日志是证据，不是关卡：写入失败会被返回，但本身绝不会阻止运行。
type AuditLog struct {
	dir string
	now func() time.Time
	mu  sync.Mutex
}

// NewAuditLog 打开（或准备）一个写入 `<dir>/<YYYY-MM-DD>.jsonl` 的日志。
// 调用方通常传入 `<storage>/audit`。
func NewAuditLog(dir string) *AuditLog {
	return &AuditLog{dir: dir, now: time.Now}
}

// Dir 是存放每日文件的目录。
func (l *AuditLog) Dir() string { return l.dir }

// Path 返回现在写入一条记录时会落到的文件。
func (l *AuditLog) Path() string {
	l.mu.Lock()
	now := l.now
	l.mu.Unlock()
	if now == nil {
		now = time.Now
	}
	return l.pathFor(now())
}

func (l *AuditLog) pathFor(t time.Time) string {
	return filepath.Join(l.dir, t.UTC().Format("2006-01-02")+".jsonl")
}

// Record 追加一条记录。时间戳、原因和参数都在这里规范化并脱敏，
// 因此没有调用方可以绕过清洗器。
func (l *AuditLog) Record(entry AuditEntry) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now
	if l.now != nil {
		now = l.now
	}
	stamp := now()
	if entry.TS == "" {
		entry.TS = stamp.UTC().Format(time.RFC3339)
	}
	entry.Reason = Redact(entry.Reason)
	if entry.Args != "" {
		entry.Args = RedactJSON([]byte(entry.Args))
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("security: encoding audit entry: %w", err)
	}
	if err := os.MkdirAll(l.dir, 0o700); err != nil {
		return fmt.Errorf("security: creating audit directory: %w", err)
	}
	file, err := os.OpenFile(l.pathFor(stamp), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("security: opening audit log: %w", err)
	}
	if _, err := file.Write(append(line, '\n')); err != nil {
		file.Close()
		return fmt.Errorf("security: writing audit log: %w", err)
	}
	return file.Close()
}

// Log 是 Chain 使用的便捷方法：它对参数脱敏并记录一条决定。
func (l *AuditLog) Log(session, tool string, decision Decision, reason string, args json.RawMessage) error {
	entry := AuditEntry{
		Tool:     tool,
		Decision: decision.String(),
		Reason:   reason,
		Session:  session,
	}
	if len(args) > 0 {
		entry.Args = RedactJSON(args)
	}
	return l.Record(entry)
}

// ReadAudit 读回某个文件中的条目，最新的在最后。
func ReadAudit(path string) ([]AuditEntry, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()
	var entries []AuditEntry
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var entry AuditEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			return entries, fmt.Errorf("security: parsing audit line: %w", err)
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return entries, err
	}
	return entries, nil
}
