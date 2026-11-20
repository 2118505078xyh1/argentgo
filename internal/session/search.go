package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// Hit 是转录中一条匹配的记录。
type Hit struct {
	Path    string    `json:"path"`
	ID      string    `json:"id"`
	Title   string    `json:"title"`
	Line    int       `json:"line"`
	Snippet string    `json:"snippet"`
	TS      time.Time `json:"ts"`
}

// Search 以不区分大小写的方式在 dir 下的每个 JSONL 转录中搜索 query。
// 命中按会话排序（最新的在前），再按行号排序（升序）。
// limit 限制命中总数；值 <= 0 表示不设上限。
//
// 片段在返回前会经过 security.Redact。session 包可以导入 internal/security 而不产生循环
// （security 只依赖标准库），这使脱敏与审计日志保持一致，而无需在此维护第二套正则。
func Search(dir, query string, limit int) ([]Hit, error) {
	needle := strings.TrimSpace(query)
	if needle == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("session: read %s: %w", dir, err)
	}

	type transcript struct {
		path    string
		id      string
		title   string
		updated time.Time
		lines   []numberedRecord
	}
	files := make([]transcript, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), fileExt) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		lines, err := readNumberedRecords(path)
		if err != nil {
			continue
		}
		// 上面的后缀匹配不区分大小写，因此像 Container.List 一样按长度剥离。
		// 区分大小写的 TrimSuffix 会在 "SESSION.JSONL" 的 id 上留下扩展名，
		// 产生一个再也无法通过 Store.OpenSession 解析的 Hit.ID。
		id := entry.Name()[:len(entry.Name())-len(fileExt)]
		records := make([]Record, 0, len(lines))
		for _, line := range lines {
			records = append(records, line.record)
		}
		// 命中可能位于任何记录的载荷中，因此仍必须扫描每一份转录。
		// 旁车文件省去了元数据的重算（标题和轮次计数），且只在其 stat 仍匹配时才被信任。
		info, statErr := entry.Info()
		var meta Meta
		cached := false
		if statErr == nil {
			meta, cached = loadIndex(path, info, id)
		}
		if !cached {
			meta = metaFromRecords(id, records)
			if statErr == nil {
				saveIndex(path, info, meta)
			}
		}
		updated := meta.Updated
		if updated.IsZero() && statErr == nil {
			updated = info.ModTime()
		}
		files = append(files, transcript{path: path, id: id, title: meta.Title, updated: updated, lines: lines})
	}
	sort.SliceStable(files, func(i, j int) bool {
		if files[i].updated.Equal(files[j].updated) {
			return files[i].id > files[j].id
		}
		return files[i].updated.After(files[j].updated)
	})

	hits := make([]Hit, 0)
	for _, file := range files {
		for _, line := range file.lines {
			snippet, ok := snippetFor(line.record.Data, needle)
			if !ok {
				continue
			}
			hits = append(hits, Hit{
				Path:    file.path,
				ID:      file.id,
				Title:   file.title,
				Line:    line.number,
				Snippet: snippet,
				TS:      line.record.TS,
			})
			if limit > 0 && len(hits) >= limit {
				return hits, nil
			}
		}
	}
	return hits, nil
}

// 对于已经拥有 store 的调用方，Search 也可在打开的 store 上调用。
func (s *Store) Search(query string, limit int) ([]Hit, error) {
	return Search(s.dir, query, limit)
}

// numberedRecord 保留物理 JSONL 行号：readRecords 刻意跳过格式错误的行，
// 但搜索命中必须指向用户看到的那一行。
type numberedRecord struct {
	number int
	record Record
}

func readNumberedRecords(path string) ([]numberedRecord, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("session: read %s: %w", path, err)
	}
	defer file.Close()

	out := make([]numberedRecord, 0, 16)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineLen)
	for number := 0; scanner.Scan(); {
		number++
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		var record Record
		if err := json.Unmarshal([]byte(text), &record); err != nil {
			continue
		}
		out = append(out, numberedRecord{number: number, record: record})
	}
	if err := scanner.Err(); err != nil {
		return out, fmt.Errorf("session: read %s: %w", path, err)
	}
	// 与 readRecords 和 ReadPage 相同的延迟、无冲突合成 id
	// （session 审计 #5）；物理行号保持不变。
	records := make([]Record, len(out))
	for i := range out {
		records[i] = out[i].record
	}
	for i := range out {
		if out[i].record.ID == "" {
			out[i].record.ID = nextID(records)
			records[i].ID = out[i].record.ID
		}
	}
	return out, nil
}

// snippetFor 在记录载荷中查找第一个包含 needle 的字符串值，
// 并返回其周围经过脱敏的单行窗口。工具输出也包含在内，因为它在载荷中是作为普通字符串值存储的。
func snippetFor(raw json.RawMessage, needle string) (string, bool) {
	wanted := strings.ToLower(needle)
	for _, value := range stringValues(raw) {
		lowered := strings.ToLower(value)
		if index := strings.Index(lowered, wanted); index >= 0 {
			// strings.Index 返回的是 lowered 中的字节偏移，但小写化可能改变字节长度
			// （例如 İ→i、ẞ→ß）。先将其转换为符文偏移：strings.ToLower 按符文逐一折叠，
			// 因此符文索引在 lowered 和原始值中完全相同。
			runeIndex := utf8.RuneCountInString(lowered[:index])
			return contextWindow(value, runeIndex, len([]rune(needle))), true
		}
	}
	return "", false
}

// stringValues 遍历 JSON 载荷并返回其中包含的所有字符串。
func stringValues(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	var out []string
	collectStrings(value, &out)
	return out
}

func collectStrings(value any, out *[]string) {
	switch typed := value.(type) {
	case string:
		*out = append(*out, typed)
	case map[string]any:
		// JSON 对象解码为 Go map，其迭代顺序是随机的。直接遍历它会使"第一个包含 needle 的
		// 字符串值"（见 snippetFor）依赖于 map 的随机种子，因此同一转录的两次搜索可能返回不同的片段。
		// 改为按排序后的顺序访问键：字符串集合不变，但顺序——以及所选择的片段——对给定记录是确定的。
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			collectStrings(typed[key], out)
		}
	case []any:
		for _, child := range typed {
			collectStrings(child, out)
		}
	}
}

// contextWindow 在符文索引周围切出片段而不破坏 UTF-8，然后在折叠空白之前对凭据脱敏。
// 脱敏发生在截断之前，因此 token 占位符永远不会被切成两半。
func contextWindow(value string, runeIndex, needleRunes int) string {
	runes := []rune(value)
	index := runeIndex
	start := index - 48
	if start < 0 {
		start = 0
	}
	end := index + needleRunes + 96
	if end > len(runes) {
		end = len(runes)
	}
	window := string(runes[start:end])
	if start > 0 {
		window = "…" + window
	}
	if end < len(runes) {
		window += "…"
	}
	return truncateRunes(security.Redact(window), 160)
}
