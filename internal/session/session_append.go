package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// Append 写入一条记录作为活动叶节点的子节点，并使其成为新的活动叶节点。
// 载荷编码为 JSON；nil 存储为 `null`。
func (s *Session) Append(kind string, payload any) error {
	_, err := s.AppendRecord(kind, payload)
	return err
}

// AppendRecord 像 Append 一样写入一条记录，但还返回存储的记录，
// 使调用方可以把后续记录（检查点、重试、尝试）链接到刚生成的确切 id。
func (s *Session) AppendRecord(kind string, payload any) (Record, error) {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return Record{}, errors.New("session: empty record kind")
	}
	data, err := encodePayload(payload)
	if err != nil {
		return Record{}, err
	}
	// 自动为持久 kind（checkpoint/summary/compaction/title）打标记，
	// 使压缩和子上下文选择器能找到它们。默认 kind 保持逐字节一致，
	// 因此普通消息/工具写入不受影响。
	data = StampIfImportant(kind, data)

	s.mu.Lock()
	defer s.mu.Unlock()

	// prepareAppend 在任何内容写入磁盘之前先生成 id 并递增 nextSeq。
	// 保留追加前的值，使写入失败时可以撤销这一偏移，让索引精确描述文件中的内容。
	prevNextSeq := s.nextSeq

	parent, id, err := s.prepareAppend()
	if err != nil {
		return Record{}, err
	}
	// Records() 始终从磁盘重新读取，因此 `active`、`lastID` 以及该记录的 `ids` 条目
	// 只在写入成功后才推进。失败时留下的唯一内存改动是递增后的 nextSeq；
	// 也要从索引中删除已生成的 id，以防写入方在报错前把它加了进去。
	// writeAppend 在写入本身失败时已经丢弃 fd/缓存，因此下一次追加会安全地从磁盘重建。
	rollback := func() {
		s.nextSeq = prevNextSeq
		if s.ids != nil {
			delete(s.ids, id)
		}
	}
	record := Record{
		ID:     id,
		Parent: parent,
		TS:     time.Now().UTC(),
		Kind:   kind,
		Data:   data,
	}
	line, err := json.Marshal(record)
	if err != nil {
		rollback()
		return Record{}, fmt.Errorf("session: encode record: %w", err)
	}
	buf := make([]byte, 0, len(line)+1)
	buf = append(buf, line...)
	buf = append(buf, '\n')
	if err := s.writeAppend(record.ID, buf); err != nil {
		rollback()
		return Record{}, err
	}
	s.active = record.ID
	return record, nil
}

// prepareAppend 返回下一条记录的 parent 和新的 id。它惰性构建内存索引，
// 并在追加 fd 缺失或过期时重建。id 规则与 nextID 相同：最小的未使用 n%06d，
// 因此有间隙的外部记录不会导致冲突。
func (s *Session) prepareAppend() (parent, id string, err error) {
	if !s.appendStateValid() {
		if err := s.rebuildAppendState(); err != nil {
			return "", "", err
		}
	}
	parent = s.active
	if _, ok := s.ids[parent]; !ok {
		// 与旧的全量读取路径相同的回退：不在文件中的活动 id（分叉、外部重写）
		// 链接到最后一条记录。
		parent = s.lastID
	}
	id = fmt.Sprintf("n%06d", s.nextSeq)
	for {
		if _, used := s.ids[id]; !used {
			break
		}
		s.nextSeq++
		id = fmt.Sprintf("n%06d", s.nextSeq)
	}
	s.nextSeq++
	return parent, id, nil
}

// appendStateValid 检查缓存索引是否仍描述磁盘上的文件：相同的 inode/卷、大小和修改时间。
// 通过重命名替换、截断或其他写入方的编辑都会让其中一项检查失败，
// 因此调用方回退到全量读取的重建路径。Windows 上没有持有的 fd，
// 因此用上次同步时记录的路径标识来做 inode 检查。
func (s *Session) appendStateValid() bool {
	if s.ids == nil {
		return false
	}
	pathInfo, err := os.Stat(s.path())
	if err != nil {
		return false
	}
	if pathInfo.Size() != s.fdSize || !pathInfo.ModTime().Equal(s.fdMTime) {
		return false
	}
	if s.fd != nil {
		fdInfo, err := s.fd.Stat()
		if err != nil {
			return false
		}
		if !os.SameFile(fdInfo, pathInfo) || fdInfo.Size() != s.fdSize {
			return false
		}
	} else if s.lastInfo == nil || !os.SameFile(pathInfo, s.lastInfo) {
		return false
	}
	return true
}

// rebuildAppendState 是全量读取回退路径：读取一次转录，重建 id 索引，
// 然后打开一个新的追加 fd。当文件在读取和 fstat 之间发生变化时会重试，
// 因此索引永远不会落后于文件中已有的字节。
func (s *Session) rebuildAppendState() error {
	s.closeAppendFD()
	for attempt := 0; attempt < 3; attempt++ {
		pathInfo, err := os.Stat(s.path())
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("session: stat %s: %w", s.path(), err)
		}
		var expectedSize int64
		if err == nil {
			expectedSize = pathInfo.Size()
		}

		records, err := s.readAll()
		if err != nil {
			return err
		}
		s.ids = make(map[string]struct{}, len(records)+8)
		s.lastID = ""
		for _, record := range records {
			s.ids[record.ID] = struct{}{}
			s.lastID = record.ID
		}
		s.nextSeq = 1
		for {
			if _, used := s.ids[fmt.Sprintf("n%06d", s.nextSeq)]; !used {
				break
			}
			s.nextSeq++
		}

		fd, err := s.openAppendFile()
		if err != nil {
			return err
		}
		info, err := fd.Stat()
		if err != nil {
			fd.Close()
			return fmt.Errorf("session: stat %s: %w", s.path(), err)
		}
		if info.Size() != expectedSize {
			// 另一个写入方在读取和打开之间移动了文件；
			// 重新开始，而不是信任过期的索引。
			fd.Close()
			continue
		}
		// 在打开之后对路径执行 Stat：O_CREATE 可能刚刚创建了文件，
		// 因此这是第一个可靠的标识/修改时间快照。
		postInfo, err := os.Stat(s.path())
		if err != nil {
			fd.Close()
			return fmt.Errorf("session: stat %s: %w", s.path(), err)
		}
		if !os.SameFile(info, postInfo) || postInfo.Size() != expectedSize {
			fd.Close()
			continue
		}
		if !keepAppendFD {
			// 见 keepAppendFD：绝不让 Windows 句柄保持打开。
			fd.Close()
			fd = nil
		}
		s.fd = fd
		s.fdSize = expectedSize
		s.fdMTime = postInfo.ModTime()
		s.lastInfo = postInfo
		return nil
	}
	return fmt.Errorf("session: %s kept changing while rebuilding append state", s.path())
}

func (s *Session) openAppendFile() (*os.File, error) {
	fd, err := os.OpenFile(s.path(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("session: open %s: %w", s.path(), err)
	}
	return fd, nil
}

// writeAppend 写入一行 JSONL 并更新内存索引。在 Unix 上复用长生命周期的 fd；
// 在 Windows 上每次追加都打开短生命周期的 fd（见 keepAppendFD）。
// 任何异常都会丢弃缓存状态，使下一次追加走安全的全量读取路径。
func (s *Session) writeAppend(id string, buf []byte) error {
	fd := s.fd
	temporary := fd == nil
	if temporary {
		var err error
		fd, err = s.openAppendFile()
		if err != nil {
			return err
		}
	}
	n, err := fd.Write(buf)
	if temporary {
		fd.Close()
	}
	if err != nil {
		s.closeAppendFD()
		s.ids = nil
		return fmt.Errorf("session: write %s: %w", s.path(), err)
	}
	if n != len(buf) {
		s.closeAppendFD()
		s.ids = nil
		return fmt.Errorf("session: write %s: short write", s.path())
	}
	s.ids[id] = struct{}{}
	s.lastID = id

	expected := s.fdSize + int64(len(buf))
	// 每次追加做一次 stat 就足以发现背后被替换的文件。
	// 大小检查证明写入落在了当前位于该路径的文件上；
	// 与 lastInfo（在 fd 打开时捕获，此后每次追加都刷新）的 inode 比较，
	// 证明该文件仍是 fd 所写入的那个。之前的 fd.Stat() 交叉检查没有覆盖上面这对检查遗漏的任何情况
	// ——路径的任何替换都会表现为大小或 inode 不匹配——反而让每次追加的系统调用成本翻倍。
	pathInfo, err := os.Stat(s.path())
	if err != nil || s.lastInfo == nil ||
		!os.SameFile(pathInfo, s.lastInfo) || pathInfo.Size() != expected {
		// 在追加期间，文件在我们背后被替换或写入。这一行写入了一个已不再有效的句柄；
		// 丢弃缓存，使下一次追加根据磁盘上的真实内容重建。
		s.invalidateAppendState()
		return nil
	}
	s.fdSize = expected
	s.fdMTime = pathInfo.ModTime()
	s.lastInfo = pathInfo
	return nil
}

// invalidateAppendState 丢弃 fd 和 id 索引。
func (s *Session) invalidateAppendState() {
	s.closeAppendFD()
	s.ids = nil
	s.lastID = ""
}

func (s *Session) closeAppendFD() {
	if s.fd != nil {
		s.fd.Close()
		s.fd = nil
	}
	s.fdSize = 0
	s.fdMTime = time.Time{}
	s.lastInfo = nil
}

// readAll 通过可覆盖的读取器，使测试可以统计全量读取次数。
func (s *Session) readAll() ([]Record, error) {
	if s.readRecordsFn != nil {
		return s.readRecordsFn(s.path())
	}
	return readRecords(s.path())
}

// invalidateAppendCacheAfterRewrite 丢弃缓存的追加状态。重写会话文件的代码
// （压缩、修复工具）应调用它，使下一次追加重新读取文件。
// 校验通常能通过 inode/大小/修改时间检测到此类重写，但这让处理变得明确且即时。
func (s *Session) invalidateAppendCacheAfterRewrite() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.invalidateAppendState()
}
