package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// defaultPageMaxBytes bounds a page when the caller passes maxBytes <= 0.
// It keeps a single read well below the size of a large transcript while still
// returning a useful amount of history.
const defaultPageMaxBytes = 4 << 20

// Page is one byte-bounded slice of a transcript, in file order.
//
// Bytes is the sum of the raw line lengths (newline excluded) of Records: the
// number of transcript bytes the page carries, not decoded payload sizes.
// HasMore reports whether more records exist on the unread side of the page.
// Next is the cursor for the next call: with fromEnd=false it is the last
// returned record's ID (resume AFTER it); with fromEnd=true it is the first
// returned record's ID (resume BEFORE it). Next is empty when HasMore is false.
type Page struct {
	Records []Record
	Next    string
	HasMore bool
	Bytes   int
}

// parsedRecord pairs a decoded record with the size of its line on disk.
type parsedRecord struct {
	record Record
	size   int
}

// ReadPage returns one page of at most maxBytes of transcript data. When
// maxBytes <= 0, defaultPageMaxBytes is used.
//
// afterID is an exclusive cursor. With fromEnd=false, records strictly after
// that ID are returned; with fromEnd=true, records strictly before it are
// returned, so a caller can walk backwards by feeding Next straight back in.
// If the cursor ID is absent, the page starts at the beginning (forward) or
// at the end of the file (backward). Records on the far side of the cursor are
// still parsed to locate it but are never counted toward maxBytes or returned.
//
// Lines are decoded leniently, exactly like readRecords: blank and malformed
// lines are skipped. A record whose encoded line alone exceeds maxBytes is
// never split — it is returned as the page's only record — so every page holds
// whole records and no decoded record is ever truncated.
func ReadPage(path string, afterID string, maxBytes int, fromEnd bool) (Page, error) {
	limit := maxBytes
	if limit <= 0 {
		limit = defaultPageMaxBytes
	}

	parsed, err := parseRecordsForPage(path)
	if err != nil {
		return Page{}, err
	}

	start, end := 0, len(parsed)
	if afterID != "" {
		for i := range parsed {
			if parsed[i].record.ID == afterID {
				if fromEnd {
					end = i
				} else {
					start = i + 1
				}
				break
			}
		}
	}

	if fromEnd {
		return pageFromEnd(parsed[:end], limit), nil
	}
	return pageFromStart(parsed[start:], limit), nil
}

// parseRecordsForPage decodes the whole file in file order. No index exists
// yet, so locating a cursor requires a full lenient pass; the returned page
// itself is still bounded by the byte limit.
func parseRecordsForPage(path string) ([]parsedRecord, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("session: read %s: %w", path, err)
	}
	defer file.Close()

	parsed := make([]parsedRecord, 0, 16)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineLen)
	valid := 0
	for scanner.Scan() {
		raw := scanner.Bytes()
		line := strings.TrimSpace(string(raw))
		if line == "" {
			continue
		}
		var record Record
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			continue
		}
		if record.ID == "" {
			record.ID = fmt.Sprintf("n%06d", valid+1)
		}
		parsed = append(parsed, parsedRecord{record: record, size: len(raw)})
		valid++
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("session: read %s: %w", path, err)
	}
	return parsed, nil
}

// pageFromStart returns the leading page of records, bounded by limit.
func pageFromStart(records []parsedRecord, limit int) Page {
	page := Page{Records: make([]Record, 0)}
	for i := range records {
		rec := records[i]
		if len(page.Records) > 0 && page.Bytes+rec.size > limit {
			page.HasMore = true
			page.Next = page.Records[len(page.Records)-1].ID
			return page
		}
		page.Records = append(page.Records, rec.record)
		page.Bytes += rec.size
		if rec.size > limit {
			// An oversized record cannot be split: return it whole and alone.
			if i+1 < len(records) {
				page.HasMore = true
				page.Next = rec.record.ID
			}
			return page
		}
	}
	return page
}

// pageFromEnd returns the trailing page of records, bounded by limit. Next is
// the first returned record's ID so a caller can step further backwards.
func pageFromEnd(records []parsedRecord, limit int) Page {
	page := Page{Records: make([]Record, 0)}
	total := 0
	first := len(records)
	for i := len(records) - 1; i >= 0; i-- {
		rec := records[i]
		if total > 0 && total+rec.size > limit {
			break
		}
		first = i
		total += rec.size
		if rec.size > limit {
			// An oversized record cannot be split: return it whole and alone.
			break
		}
	}
	if first < len(records) {
		page.Records = make([]Record, 0, len(records)-first)
		for i := first; i < len(records); i++ {
			page.Records = append(page.Records, records[i].record)
		}
		page.Bytes = total
	}
	if first > 0 {
		page.HasMore = true
		page.Next = records[first].record.ID
	}
	return page
}
