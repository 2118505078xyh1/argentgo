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
// 替换它之前的所有内容。这是**喂给模型的读路径**：默认开启时应用 foveated
// 多保真归档（TODO #134），在有限预算内保留两端逐字内容。压缩决策不要用本
// 方法估算 token（稀疏视图会低估压力），请用 MessagesForCompaction。
func (s *Session) Messages() []Entry {
	return s.messages(foveatedArchiveEnabled())
}

// MessagesForCompaction 返回未做 foveated 稀疏化的完整对话视图，供压缩决策使用。
// foveated 读路径会把中段按预算稀疏化，若用它估算 token，长会话可能因估算值
// 低于阈值而永不触发压缩——这是默认开启归档后必须规避的耦合。本方法与
// Messages() 在归档关闭时逐字节相同；归档开启时返回完整条目（仍经压缩摘要
// 折叠、孤儿结算与图像卸载投影），保证压缩判断基于真实转录体量。
func (s *Session) MessagesForCompaction() []Entry {
	return s.messages(false)
}

// messages 是 Messages / MessagesForCompaction 的共享实现。foveated 为真时
// 在压缩分支之前应用多保真投影：压缩分支会把 head 截到 compaction.Keep 条，
// 旧端已经丢失，无法实现"两端高保真"，因此归档必须直接基于全量 path 构造。
func (s *Session) messages(foveated bool) []Entry {
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

	if foveated {
		budget, _ := budgetProjectionBudget()
		all := s.projectedEntries(path)
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
		return finalizeMessageView(archived, path)
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
			return finalizeMessageView(out, path)
		}
	}
	// 同上：ReconcileToolPairs 已在 messageEntries 中插入中止结果，
	// 这里再补上原因元数据与稳定短 id；正常会话原样返回。
	return finalizeMessageView(s.projectedEntries(path), path)
}

// finalizeMessageView 收口消息视图的最后两步：结算孤儿调用 → 图像卸载。
// 图像卸载是记录标记驱动的投影，无标记时逐字节不变；省略告示不属于 Messages()
// 的稳定会话视图，它由 MessagesWithOmissionNotice 单独提供给下一轮注入点。
func finalizeMessageView(entries []Entry, path []Record) []Entry {
	settled := SettleOrphanToolCalls(entries, SettleInterrupted)
	return applyImageOffload(settled, ImageOffloadsIn(path))
}

// MessagesWithOmissionNotice 返回下一轮使用的对话视图：在 Messages() 之上，
// 当本轮确实发生 prune/隐藏/预算投影丢消息时，追加一条「缺失不构成授权」告示。
// 它是默认开启的下一轮注入入口；干净路径与 Messages() 逐字节相同。
func (s *Session) MessagesWithOmissionNotice() []Entry {
	messages := s.Messages()
	records, err := s.Records()
	if err != nil {
		return messages
	}
	return withOmissionNotice(messages, projectionOmission(records, s.activePath(records), messages))
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
		// 统一解码边界（#189）：与旧 json.Unmarshal 逐字节等价，坏行同样宽松跳过。
		event, ok, err := DecodeRecord(line)
		if err != nil || !ok {
			continue
		}
		records = append(records, event.Record)
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
	// 未知 kind 必须显式标记 ignorable，否则拒绝重建（#186：禁止静默丢弃事件）。
	// 校验在迁移之前进行：迁移只加 schema 头，不改变 kind 集合。
	if err := CheckUnknownKinds(records); err != nil {
		return nil, err
	}
	return MigrateRecords(records)
}
