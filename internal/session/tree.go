package session

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// Branch 描述会话 id/parent 树中的一条记录。
type Branch struct {
	ID     string
	Parent string
	Kind   string
	Depth  int  // number of parent hops from the root
	Active bool // true for the leaf the transcript currently follows
	Label  string
	// SourceSession 和 SourceRecord 为 retry/fork 的尝试记录而设置。
	SourceSession string
	SourceRecord  string
}

// Branches 描述存储在 path 处 JSONL 文件中的记录树。记录按文件顺序返回，
// 该顺序在分叉和压缩之后保持稳定。活动叶子是最后一条记录，与打开的
// Session 恢复时的方式一致。
func Branches(path string) ([]Branch, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("session: open %s: %w", path, err)
	}
	records, err := readRecords(path)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, nil
	}
	byID := indexByID(records)
	active := records[len(records)-1].ID
	depths := make(map[string]int, len(records))
	out := make([]Branch, 0, len(records))
	for _, record := range records {
		branch := Branch{
			ID:     record.ID,
			Parent: record.Parent,
			Kind:   record.Kind,
			Depth:  branchDepth(record.ID, byID, depths),
			Active: record.ID == active,
			Label:  branchLabel(record),
		}
		if record.Kind == KindAttempt {
			var attempt Attempt
			if jsonUnmarshal(record.Data, &attempt) == nil {
				branch.SourceSession = attempt.SourceSession
				branch.SourceRecord = attempt.SourceRecord
			}
		}
		out = append(out, branch)
	}
	return out, nil
}

// Branches 在 store 中解析一个 id（或唯一前缀）并描述它。
func (s *Store) Branches(id string) ([]Branch, error) {
	path, err := s.ResolvePath(id)
	if err != nil {
		return nil, err
	}
	return Branches(path)
}

func branchDepth(id string, byID map[string]Record, cache map[string]int) int {
	origID := id
	if depth, ok := cache[id]; ok {
		return depth
	}
	depth := 0
	seen := map[string]bool{id: true}
	for record, ok := byID[id]; ok && record.Parent != ""; {
		if seen[record.Parent] {
			break
		}
		seen[record.Parent] = true
		depth++
		if cached, ok := cache[record.Parent]; ok {
			depth += cached
			break
		}
		id, record = record.Parent, byID[record.Parent]
	}
	// 以供查询深度的那个 id 作为缓存键，而不是遍历最终到达的最顶层祖先：
	// 把累积深度存在祖先身上会破坏之后每一个从它派生出来的分支
	// （session 审计 #6）。
	cache[origID] = depth
	return depth
}

func branchLabel(record Record) string {
	if entry, ok := messageEntry(record); ok {
		return truncateRunes(entry.Content, 48)
	}
	if record.Kind == KindAttempt {
		var attempt Attempt
		if jsonUnmarshal(record.Data, &attempt) == nil {
			return truncateRunes(attempt.Reason+" → "+attempt.Snippet(), 48)
		}
	}
	if record.Kind == KindCompaction {
		var compaction Compaction
		if json.Unmarshal(record.Data, &compaction) == nil {
			return truncateRunes(compaction.Summary, 48)
		}
	}
	return record.Kind
}

// ResolvePath 把精确的 id 或唯一前缀映射到其 JSONL 文件，而不创建目录，
// 使只读命令绝不会触碰文件系统。
func ResolvePath(dir, id string) (string, error) {
	return (&Container{dir: dir}).resolvePath(id)
}

// ResolvePath 是包级辅助函数的 store 作用域版本。
func (s *Store) ResolvePath(id string) (string, error) {
	return ResolvePath(s.dir, id)
}

// RenderHTML 写出以 branchID 结尾的分支的独立 HTML 转录。branchID 为空时
// 跟随活动叶子。输出只有内联 CSS，没有脚本和网络引用，且每个文本值都由
// html/template 转义。
func RenderHTML(path, branchID string, w io.Writer) error {
	records, err := readRecords(path)
	if err != nil {
		return err
	}
	if len(records) == 0 {
		return fmt.Errorf("session: no records in %s", path)
	}
	byID := indexByID(records)
	leaf := branchID
	if leaf == "" {
		leaf = records[len(records)-1].ID
	}
	// 拒绝未知的分支，而不是静默地渲染活动分支。
	if _, ok := byID[leaf]; !ok {
		return fmt.Errorf("session: unknown branch %q in %s", branchID, path)
	}

	chain := make([]Record, 0, len(records))
	seen := make(map[string]bool, len(records))
	for current := leaf; current != ""; {
		record, ok := byID[current]
		if !ok || seen[current] {
			break
		}
		seen[current] = true
		chain = append(chain, record)
		current = record.Parent
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}

	id, title := "", ""
	for _, record := range chain {
		if record.Kind == KindHeader {
			id = record.ID
			title = fieldString(record.Data, "title")
		}
	}
	if id == "" {
		// 与发现和打开转录时使用的不区分大小写的扩展名处理保持一致；
		// 见 store.go 中的 transcriptStem。
		id = transcriptStem(filepath.Base(path))
	}
	if title == "" {
		title = id
	}

	messages := make([]htmlMessage, 0, len(chain))
	attempts := make([]htmlAttempt, 0)
	for _, record := range chain {
		if record.Kind == KindAttempt {
			var attempt Attempt
			if jsonUnmarshal(record.Data, &attempt) == nil {
				attempts = append(attempts, htmlAttempt{
					Reason: security.Redact(attempt.Reason),
					Text:   security.Redact(attempt.Snippet()),
					Time:   formatHTMLTime(record.TS),
				})
			}
		}
		message, ok := messageEntry(record)
		if !ok {
			if record.Kind == KindCompaction {
				var compaction Compaction
				if json.Unmarshal(record.Data, &compaction) == nil && compaction.Summary != "" {
					message = Entry{Role: "system", Content: compaction.Summary, TS: record.TS}
					ok = true
				}
			}
		}
		if !ok {
			continue
		}
		role := message.Role
		switch role {
		case "user", "assistant", "tool", "system":
		default:
			role = "system"
		}
		messages = append(messages, htmlMessage{
			Role:    role,
			Class:   "role-" + role,
			Name:    message.Name,
			Content: message.Content,
			Time:    formatHTMLTime(message.TS),
		})
	}

	data := struct {
		Title    string
		Branch   string
		Messages []htmlMessage
		Attempts []htmlAttempt
	}{Title: title, Branch: leaf, Messages: messages, Attempts: attempts}
	if err := htmlPage.Execute(w, data); err != nil {
		return fmt.Errorf("session: render %s: %w", path, err)
	}
	return nil
}

// RenderHTML 把存储中某个会话 id 的活动分支写到 w。
func (s *Store) RenderHTML(id, branchID string, w io.Writer) error {
	path, err := s.ResolvePath(id)
	if err != nil {
		return err
	}
	return RenderHTML(path, branchID, w)
}

type htmlMessage struct {
	Role    string
	Class   string
	Name    string
	Content string
	Time    string
}

type htmlAttempt struct {
	Reason string
	Text   string
	Time   string
}

func formatHTMLTime(ts time.Time) string {
	if ts.IsZero() {
		return ""
	}
	return ts.UTC().Format(time.RFC3339)
}

var htmlPage = template.Must(template.New("session").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>{{.Title}}</title>
<style>
body{margin:0;padding:24px;background:#0e1117;color:#d7dee9;font:14px/1.6 system-ui,sans-serif}
h1{font-size:20px;margin:0 0 4px}
.meta{color:#7f8b9f;margin-bottom:20px}
.msg{margin:0 0 12px;padding:10px 14px;border-radius:8px;border-left:4px solid #555;background:#161b25;white-space:pre-wrap;word-break:break-word}
.role-user{border-left-color:#7aa2f7}
.role-assistant{border-left-color:#9ece6a}
.role-tool{border-left-color:#e0af68}
.role-system{border-left-color:#bb9af7}
.role-attempt{border-left-color:#f7768e;color:#e0a0a8}
.role{font-size:11px;text-transform:uppercase;letter-spacing:.08em;color:#7f8b9f;margin-bottom:4px}
.name{color:#e0af68;margin-right:8px}
</style></head><body>
<h1>{{.Title}}</h1><div class="meta">branch {{.Branch}}</div>
{{range .Messages}}<div class="msg {{.Class}}"><div class="role">{{.Role}}</div>{{if .Name}}<span class="name">{{.Name}}</span>{{end}}{{.Content}}{{if .Time}}<div class="role">{{.Time}}</div>{{end}}</div>
{{end}}{{range .Attempts}}<div class="msg role-attempt"><div class="role">attempt · {{.Reason}}</div>{{.Text}}{{if .Time}}<div class="role">{{.Time}}</div>{{end}}</div>
{{end}}</body></html>
`))
