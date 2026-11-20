package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Records returns every record in file order.
func (s *Session) Records() ([]Record, error) {
	return readRecords(s.path())
}

// ReadPage returns one byte-bounded page of records from this session's file.
// It is a lenient read API: malformed lines are skipped and the cursor is
// resolved by scanning the file, without modifying the transcript.
func (s *Session) ReadPage(afterID string, maxBytes int, fromEnd bool) (Page, error) {
	return ReadPage(s.path(), afterID, maxBytes, fromEnd)
}

// Messages returns the linear conversation on the active branch. The latest
// compaction record replaces everything before it with one system summary.
func (s *Session) Messages() []Entry {
	records, err := s.Records()
	if err != nil {
		return nil
	}
	path := s.activePath(records)

	latest := -1
	for i := range path {
		if path[i].Kind == KindCompaction {
			latest = i
		}
	}
	if latest >= 0 {
		var compaction Compaction
		if err := json.Unmarshal(path[latest].Data, &compaction); err == nil {
			out := []Entry{{
				ID:      path[latest].ID,
				Parent:  path[latest].Parent,
				Role:    "system",
				Content: compaction.Summary,
				TS:      path[latest].TS,
				Data:    path[latest].Data,
			}}
			head := messageEntries(path[:latest])
			if compaction.Keep >= 0 && compaction.Keep < len(head) {
				head = head[len(head)-compaction.Keep:]
			}
			out = append(out, head...)
			out = append(out, messageEntries(path[latest+1:])...)
			return out
		}
	}
	return messageEntries(path)
}

// readRecords parses a transcript. Blank and malformed lines are skipped so a
// torn append cannot make the whole session unreadable.
func readRecords(path string) ([]Record, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("session: read %s: %w", path, err)
	}
	defer file.Close()

	records := make([]Record, 0, 16)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineLen)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record Record
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			continue
		}
		records = append(records, record)
	}
	// Mint synthetic ids only after the whole file is parsed, so an explicit
	// id appearing later cannot collide with one minted for an earlier record
	// that had no id (session audit #5). nextID skips every id already present.
	for i := range records {
		if records[i].ID == "" {
			records[i].ID = nextID(records)
		}
	}
	if err := scanner.Err(); err != nil {
		return records, fmt.Errorf("session: read %s: %w", path, err)
	}
	return records, nil
}
