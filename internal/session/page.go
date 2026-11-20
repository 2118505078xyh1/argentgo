package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// defaultPageMaxBytes 在调用方传入 maxBytes <= 0 时限制页大小。
// 它使单次读取远低于大转录的大小，同时仍返回有用的历史量。
const defaultPageMaxBytes = 4 << 20

// Page 是按文件顺序、受字节限制的转录片段。
//
// Bytes 是 Records 原始行长度之和（不含换行符）：该页携带的转录字节数，
// 不是解码后载荷的大小。HasMore 报告页的未读一侧是否还有更多记录。
// Next 是下一次调用的游标：fromEnd=false 时是最后一条返回记录的 ID（在其后恢复）；
// fromEnd=true 时是第一条返回记录的 ID（在其前恢复）。HasMore 为 false 时 Next 为空。
type Page struct {
	Records []Record
	Next    string
	HasMore bool
	Bytes   int
}

// parsedRecord 将解码后的记录与其磁盘上的行大小配对。
type parsedRecord struct {
	record Record
	size   int
}

// ReadPage 返回一页最多 maxBytes 字节的转录数据。当 maxBytes <= 0 时，使用 defaultPageMaxBytes。
//
// afterID 是排他游标。fromEnd=false 时返回严格位于该 ID 之后的记录；
// fromEnd=true 时返回严格位于其之前的记录，因此调用方可以把 Next 直接传回以向前（往回）遍历。
// 如果游标 ID 不存在，页从文件开头（正向）或文件末尾（反向）开始。
// 游标另一侧的记录仍会被解析以定位它，但从不计入 maxBytes，也不会返回。
//
// 行的解码是宽松的，与 readRecords 完全一致：空白行和格式错误的行会被跳过。
// 仅编码后的一行就超过 maxBytes 的记录永远不会被拆分——它作为该页唯一的记录返回——
// 因此每页都包含完整的记录，任何解码后的记录都不会被截断。
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

// parseRecordsForPage 按文件顺序解码整个文件。目前还没有索引，
// 因此定位游标需要一次完整的宽松扫描；返回的页本身仍受字节上限约束。
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
		parsed = append(parsed, parsedRecord{record: record, size: len(raw)})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("session: read %s: %w", path, err)
	}
	// 延迟生成合成 id，直到所有显式 id 都已知，然后使用与 readRecords 相同的辅助函数，
	// 使一个读取 API 生成的游标能在另一个中解析（session 审计 #5）。
	records := make([]Record, len(parsed))
	for i := range parsed {
		records[i] = parsed[i].record
	}
	for i := range parsed {
		if parsed[i].record.ID == "" {
			parsed[i].record.ID = nextID(records)
			records[i].ID = parsed[i].record.ID
		}
	}
	return parsed, nil
}

// pageFromStart 返回开头的记录页，受 limit 限制。
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
			// 超大记录不能拆分：整条单独返回。
			if i+1 < len(records) {
				page.HasMore = true
				page.Next = rec.record.ID
			}
			return page
		}
	}
	return page
}

// pageFromEnd 返回末尾的记录页，受 limit 限制。Next 是第一条返回记录的 ID，
// 使调用方可以继续往回翻页。
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
			// 超大记录不能拆分：整条单独返回。
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
