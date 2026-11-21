package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Path 返回从会话根到 entryID 的记录，最早的在前。
func (s *Session) Path(entryID string) ([]Record, error) {
	records, err := s.Records()
	if err != nil {
		return nil, err
	}
	byID := indexByID(records)
	chain := make([]Record, 0, len(records))
	seen := make(map[string]bool, len(records))
	for current := entryID; current != ""; {
		record, ok := byID[current]
		if !ok {
			return nil, fmt.Errorf("session: unknown entry %q", current)
		}
		if seen[current] {
			return nil, fmt.Errorf("session: cycle at entry %q", current)
		}
		seen[current] = true
		chain = append(chain, record)
		current = record.Parent
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain, nil
}

// Fork 将到 entryID 的路径复制到一个新会话并返回。原会话不受影响，
// 副本保留原有的条目 id，因此它所指向的历史不变。
func Fork(dir, id, entryID string) (*Session, error) {
	store, err := Open(dir)
	if err != nil {
		return nil, err
	}
	source, err := store.OpenSession(id)
	if err != nil {
		return nil, err
	}
	return source.Fork(entryID)
}

// Fork 将到 entryID 的路径复制到同一目录下的新会话。
func (s *Session) Fork(entryID string) (*Session, error) {
	path, err := s.Path(entryID)
	if err != nil {
		return nil, err
	}
	if len(path) == 0 {
		return nil, fmt.Errorf("session: entry %q is not on a path", entryID)
	}
	// 从轮次中途的记录分叉会丢失 header 及其显式标题。
	// 当路径不包含 header 时，复制原始 header，并把有效标题作为一条新的标题记录带过去。
	all, err := s.Records()
	if err != nil {
		return nil, err
	}
	hasHeader, hasTitle := false, false
	for _, record := range path {
		if record.Kind == KindHeader {
			hasHeader = true
		}
		if record.Kind == KindTitle {
			hasTitle = true
		}
	}
	if !hasHeader {
		for _, record := range all {
			if record.Kind == KindHeader {
				path = append([]Record{record}, path...)
				break
			}
		}
	}
	// 路径上存在标题记录还不够：更新的覆盖可能是在分叉点之后追加的，
	// 使路径上留下的是较旧的标题。把源会话的有效标题与复制路径所显示的标题进行比较，
	// 不同时将前者带过去，这样分叉恢复时使用的名字与会话被分叉时的名字相同。
	sourceTitle := titleFromRecords(s.ID(), all)
	pathTitle := titleFromRecords(s.ID(), path)
	if !hasTitle || pathTitle != sourceTitle {
		if title := sourceTitle; title != "" {
			data, err := encodePayload(TitleRecord{Title: title})
			if err != nil {
				return nil, err
			}
			path = append(path, Record{
				ID:     nextID(path),
				Parent: path[len(path)-1].ID,
				TS:     nextRecordTS(),
				Kind:   KindTitle,
				Data:   data,
			})
		}
	}
	container := &Container{dir: s.dir}
	if err := os.MkdirAll(container.dir, 0o755); err != nil {
		return nil, fmt.Errorf("session: create %s: %w", container.dir, err)
	}
	id, target, err := container.newID()
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("session: create %s: %w", target, err)
	}
	writer := bufio.NewWriter(file)
	for _, record := range path {
		line, err := json.Marshal(record)
		if err != nil {
			file.Close()
			os.Remove(target)
			return nil, fmt.Errorf("session: encode fork record: %w", err)
		}
		if _, err := writer.Write(append(line, '\n')); err != nil {
			file.Close()
			os.Remove(target)
			return nil, fmt.Errorf("session: write %s: %w", target, err)
		}
	}
	if err := writer.Flush(); err != nil {
		file.Close()
		os.Remove(target)
		return nil, fmt.Errorf("session: write %s: %w", target, err)
	}
	if err := file.Close(); err != nil {
		os.Remove(target)
		return nil, fmt.Errorf("session: close %s: %w", target, err)
	}
	return &Session{dir: container.dir, id: id, file: target, active: path[len(path)-1].ID}, nil
}

// path 是会话文件的位置。通过 ResolvePath 打开的会话会保留它所找到文件的确切拼写
// （可能使用大写扩展名）；只有直接由 dir+id 构造的会话才需要规范的 id+fileExt 拼写。
// 从 id 重建路径会丢失原始大小写，并在区分大小写的文件系统上破坏每一次读写。
func (s *Session) path() string {
	if s.file != "" {
		return s.file
	}
	return filepath.Join(s.dir, s.id+fileExt)
}

// activeSnapshot 在互斥锁下返回活动叶节点。每条遍历树的读取路径都经过这个辅助函数，
// 使 s.active 字段永远不会在锁外被读取，而调用方仍可在无锁状态下执行自己的文件 I/O。
func (s *Session) activeSnapshot() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

// activePath 从活动叶节点走到根节点，并返回记录（最早的在前）。
// 从叶节点不可达的记录属于分支，不会出现在对话中。
func (s *Session) activePath(records []Record) []Record {
	byID := indexByID(records)
	leaf := s.activeSnapshot()
	if _, ok := byID[leaf]; !ok {
		if len(records) == 0 {
			return nil
		}
		leaf = records[len(records)-1].ID
	}
	chain := make([]Record, 0, len(records))
	seen := make(map[string]bool, len(records))
	for current := leaf; current != ""; {
		record, ok := byID[current]
		if !ok {
			break
		}
		if seen[current] {
			break
		}
		seen[current] = true
		chain = append(chain, record)
		current = record.Parent
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain
}

func indexByID(records []Record) map[string]Record {
	index := make(map[string]Record, len(records))
	for _, record := range records {
		index[record.ID] = record
	}
	return index
}

// nextID 生成未被使用的最小 id。
func nextID(records []Record) string {
	used := make(map[string]bool, len(records))
	for _, record := range records {
		used[record.ID] = true
	}
	for n := 1; ; n++ {
		candidate := fmt.Sprintf("n%06d", n)
		if !used[candidate] {
			return candidate
		}
	}
}
