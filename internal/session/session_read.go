package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Records 按文件顺序返回所有记录。
func (s *Session) Records() ([]Record, error) {
	// 批量写模式下先落盘暂存记录，让读取看到完整、连贯的转录。
	// 默认模式下 pending 恒为空，Flush 是空操作，字节与 IO 行为不变。
	if err := s.Flush(); err != nil {
		return nil, err
	}
	return readRecords(s.path())
}

// ReadPage 从本会话的文件返回一页受字节大小限制的记录。
// 它是一个宽松的读取 API：格式错误的行会被跳过，游标通过扫描文件解析，
// 而不会修改转录。
func (s *Session) ReadPage(afterID string, maxBytes int, fromEnd bool) (Page, error) {
	return ReadPage(s.path(), afterID, maxBytes, fromEnd)
}

// Messages 返回活动分支上的线性对话。最新的压缩记录会用一条系统摘要
// 替换它之前的所有内容。
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

	// foveated 多保真归档视图（opt-in，TODO #134）：必须在压缩分支之前接线。
	// 压缩分支会把 head 截到 compaction.Keep 条，旧端已经丢失，无法实现"两端高保真"；
	// 这里直接用全量 path 重建条目再做多保真投影，并保留压缩摘要作为开头的 system 消息。
	// 关闭时本分支完全不进入，下方压缩/非压缩路径逐字节不变。
	if foveatedArchiveEnabled() {
		budget, _ := budgetProjectionBudget()
		all := messageEntries(path)
		archived := foveatedArchive(all, budget)
		if latest >= 0 {
			var compaction Compaction
			if err := json.Unmarshal(path[latest].Data, &compaction); err == nil {
				summary := Entry{
					ID: path[latest].ID, Parent: path[latest].Parent, Role: "system",
					Content: compaction.Summary, TS: path[latest].TS, Data: path[latest].Data,
				}
				archived = append([]Entry{summary}, archived...)
			}
		}
		return SettleOrphanToolCalls(archived, SettleInterrupted)
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
			// 默认开启的工具调用结算：只为孤儿调用补齐带原因的合成结果。
			// 干净会话没有孤儿也没有合成结果，结算是空操作，投影逐字节不变。
			return SettleOrphanToolCalls(out, SettleInterrupted)
		}
	}
	// 同上：ReconcileToolPairs 已在 messageEntries 中插入中止结果，
	// 这里再补上原因元数据与稳定短 id；正常会话原样返回。
	return SettleOrphanToolCalls(messageEntries(path), SettleInterrupted)
}

// readRecords 解析转录。空行和格式错误的行会被跳过，
// 使一次中断的追加不会让整个会话无法读取。
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
	// 只在解析完整个文件之后才生成合成 id，使后面出现的显式 id 不会与
	// 为更早的无 id 记录生成的 id 冲突（session 审计 #5）。nextID 会跳过
	// 所有已存在的 id。
	for i := range records {
		if records[i].ID == "" {
			records[i].ID = nextID(records)
		}
	}
	if err := scanner.Err(); err != nil {
		return records, fmt.Errorf("session: read %s: %w", path, err)
	}
	// 在读取路径上把旧版（v0）转录升级到当前 schema，使每个入口
	// （Records/Messages/Branches/...）都看到一致的视图。迁移只涉及元数据：
	// 它可能在前面加上一个 schema 头，但绝不改变对话。该头有意保留空 id：
	// 它是没有父节点的元数据，因此 id/parent 树遍历永远不会访问它
	// （两个循环都在 parent == "" 时停止），且空 id 不会与追加路径生成的
	// n%06d id 冲突。在这里生成 id 会消耗一个真实的 id 槽位，并在追加记录中
	// 留下永久的缺口。
	return MigrateRecords(records)
}
