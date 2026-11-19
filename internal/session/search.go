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

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// Hit is one matching record inside a transcript.
type Hit struct {
	Path    string    `json:"path"`
	ID      string    `json:"id"`
	Title   string    `json:"title"`
	Line    int       `json:"line"`
	Snippet string    `json:"snippet"`
	TS      time.Time `json:"ts"`
}

// Search scans every JSONL transcript under dir for query, case-insensitively.
// Hits are ordered by session (newest first) and then by line number (ascending).
// limit caps the total number of hits; a value <= 0 means no cap.
//
// Snippets are passed through security.Redact before being returned. The
// session package can import internal/security without a cycle (security only
// depends on the standard library), which keeps redaction consistent with the
// audit log instead of maintaining a second regex set here.
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
		id := strings.TrimSuffix(entry.Name(), fileExt)
		records := make([]Record, 0, len(lines))
		for _, line := range lines {
			records = append(records, line.record)
		}
		// A hit can live in any record's payload, so every transcript still has
		// to be scanned. The sidecar removes the meta recomputation (title and
		// turn counting) and is only trusted while its stat still matches.
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

// Search is also available on an open store for callers that already own one.
func (s *Store) Search(query string, limit int) ([]Hit, error) {
	return Search(s.dir, query, limit)
}

// numberedRecord keeps the physical JSONL line number: readRecords deliberately
// skips malformed lines, but a search hit must point at the line a user sees.
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
		if record.ID == "" {
			record.ID = fmt.Sprintf("n%06d", len(out)+1)
		}
		out = append(out, numberedRecord{number: number, record: record})
	}
	if err := scanner.Err(); err != nil {
		return out, fmt.Errorf("session: read %s: %w", path, err)
	}
	return out, nil
}

// snippetFor finds the first string value in a record payload containing the
// needle and returns a redacted, single-line window around it. Tool output is
// included because it is stored as a plain string value in the payload.
func snippetFor(raw json.RawMessage, needle string) (string, bool) {
	wanted := strings.ToLower(needle)
	for _, value := range stringValues(raw) {
		if index := strings.Index(strings.ToLower(value), wanted); index >= 0 {
			return contextWindow(value, index, len([]rune(needle))), true
		}
	}
	return "", false
}

// stringValues walks a JSON payload and returns every string it contains.
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
		for _, child := range typed {
			collectStrings(child, out)
		}
	case []any:
		for _, child := range typed {
			collectStrings(child, out)
		}
	}
}

// contextWindow slices a snippet around a byte index without breaking UTF-8,
// then redacts credentials before collapsing whitespace. Redaction happens
// before truncation so a token placeholder is never cut in half.
func contextWindow(value string, byteIndex, needleRunes int) string {
	runes := []rune(value)
	index := len([]rune(value[:byteIndex]))
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
