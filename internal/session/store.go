package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Meta 是会话列表无需将转录加载到内存即可显示的内容。
type Meta struct {
	ID      string    `json:"id"`
	Title   string    `json:"title"`
	Updated time.Time `json:"updated"`
	Turns   int       `json:"turns"`
	// Modified 是转录文件的 mtime，在 List 期间捕获，
	// 仅用于在记录内时间戳相同时排列会话顺序。它永不被持久化
	// （旁车文件仍在别处描述 size+mtime），因此添加它不会改变磁盘格式。
	Modified time.Time `json:"-"`
}

// Container 是存放会话文件的目录。
type Container struct {
	dir string
}

// Store 是单个会话目录的入口。
type Store struct {
	dir string
}

// Open 在 dir 不存在时创建它，并返回基于它的 store。
func Open(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("session: empty directory")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("session: create %s: %w", dir, err)
	}
	return &Store{dir: dir}, nil
}

// Dir 报告存放会话文件的目录。
func (s *Store) Dir() string { return s.dir }

// Container 返回 store 背后的目录句柄。
func (s *Store) Container() *Container { return &Container{dir: s.dir} }

// NewSession 创建只含一条 header 记录的会话文件。
func (s *Store) NewSession(title string) (*Session, error) {
	return s.Container().NewSession(title)
}

// OpenSession 按 id（精确 id 或唯一前缀）重新打开已存储的会话。
func (s *Store) OpenSession(id string) (*Session, error) {
	return s.Container().OpenSession(id)
}

// OpenSessionAt 打开会话并把活动叶节点切换到 entryID 指向的记录。
// 它等价于 OpenSession 之后紧接着调用 Checkout：先按 id 定位并校验转录，
// 再在校验通过时切换活动指针；任一步失败都不返回可用的会话句柄。
func (s *Store) OpenSessionAt(id, entryID string) (*Session, error) {
	sess, err := s.OpenSession(id)
	if err != nil {
		return nil, err
	}
	if err := sess.Checkout(entryID); err != nil {
		return nil, err
	}
	return sess, nil
}

// List 报告所有会话，最新的在前。
func (s *Store) List() ([]Meta, error) { return s.Container().List() }

// Find 报告 id 或标题包含 query 的会话。
func (s *Store) Find(query string) ([]Meta, error) { return s.Container().Find(query) }

// Delete 删除一个会话文件。
func (s *Store) Delete(id string) error { return s.Container().Delete(id) }

// OpenSession 按 id（精确 id 或唯一前缀）重新打开已存储的会话。
func OpenSession(dir, id string) (*Session, error) {
	return (&Container{dir: dir}).OpenSession(id)
}

// List 报告 dir 下的所有会话，最新的在前。
func List(dir string) ([]Meta, error) { return (&Container{dir: dir}).List() }

// Find 报告 dir 下 id 或标题包含 query 的会话。
func Find(dir, query string) ([]Meta, error) { return (&Container{dir: dir}).Find(query) }

// Delete 删除 dir 下的一个会话文件。
func Delete(dir, id string) error { return (&Container{dir: dir}).Delete(id) }

// Dir 报告存放会话文件的目录。
func (c *Container) Dir() string { return c.dir }

// NewSession 创建只含一条 header 记录的会话文件。
func (c *Container) NewSession(title string) (*Session, error) {
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return nil, fmt.Errorf("session: create %s: %w", c.dir, err)
	}
	id, path, err := c.newID()
	if err != nil {
		return nil, err
	}
	session := &Session{dir: c.dir, id: id, file: path}
	header := Header{Title: strings.TrimSpace(title), Created: time.Now().UTC()}
	if err := session.Append(KindHeader, header); err != nil {
		return nil, err
	}
	return session, nil
}

// OpenSession 按 id（精确 id 或唯一前缀）重新打开已存储的会话。
func (c *Container) OpenSession(id string) (*Session, error) {
	path, err := c.resolvePath(id)
	if err != nil {
		return nil, err
	}
	if _, err := ValidateAndRepair(path, true); err != nil {
		return nil, fmt.Errorf("resume session %q: %w", id, err)
	}
	session := &Session{
		dir:  c.dir,
		id:   transcriptStem(filepath.Base(path)),
		file: path,
	}
	records, err := readRecords(path)
	if err != nil {
		return nil, err
	}
	if len(records) > 0 {
		session.active = records[len(records)-1].ID
	}
	return session, nil
}

// List 报告所有会话，最新的在前。当存在匹配的旁车文件时不会解析转录；
// 旁车文件缺失、损坏或过期时，会回退到原始的全量扫描，并在返回前刷新。
func (c *Container) List() ([]Meta, error) {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("session: read %s: %w", c.dir, err)
	}
	metas := make([]Meta, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), fileExt) {
			continue
		}
		id := entry.Name()[:len(entry.Name())-len(fileExt)]
		path := filepath.Join(c.dir, entry.Name())

		info, statErr := entry.Info()
		var meta Meta
		var cached bool
		if statErr == nil {
			meta, cached = loadIndex(path, info, id)
		}
		if !cached {
			// 列表操作从不修复：严重损坏的转录在这里不可见，
			// 残缺的尾部会保持原样，直到显式恢复时才处理。
			if _, err := ValidateAndRepair(path, false); err != nil {
				continue
			}
			records, err := readRecords(path)
			if err != nil {
				continue
			}
			meta = metaFromRecords(id, records)
			if statErr == nil {
				saveIndex(path, info, meta)
			}
		}
		if meta.Updated.IsZero() && statErr == nil {
			meta.Updated = info.ModTime()
		}
		// 旁车文件不存储 Modified；每次 List 都捕获它，
		// 这样即使缓存命中也带有打破 Updated 并列所需的信息。
		if statErr == nil {
			meta.Modified = info.ModTime()
		}
		metas = append(metas, meta)
	}
	sort.Slice(metas, func(i, j int) bool {
		if !metas[i].Updated.Equal(metas[j].Updated) {
			return metas[i].Updated.After(metas[j].Updated)
		}
		// 记录内的时钟可能为两个会话打上相同的时间戳（例如同一秒创建的两个会话）。
		// 文件的 mtime 记录了真实的写入顺序，因此较晚的追加会把会话移到顶部；
		// 只有当两个时间戳完全相同时才回退到 id。
		if !metas[i].Modified.Equal(metas[j].Modified) {
			return metas[i].Modified.After(metas[j].Modified)
		}
		return metas[i].ID > metas[j].ID
	})
	return metas, nil
}

// Find 报告 id 或标题包含 query 的会话，最新的在前。
func (c *Container) Find(query string) ([]Meta, error) {
	all, err := c.List()
	if err != nil {
		return nil, err
	}
	needle := strings.ToLower(strings.TrimSpace(query))
	if needle == "" {
		return all, nil
	}
	matches := make([]Meta, 0, len(all))
	for _, meta := range all {
		if strings.Contains(strings.ToLower(meta.ID), needle) ||
			strings.Contains(strings.ToLower(meta.Title), needle) {
			matches = append(matches, meta)
		}
	}
	return matches, nil
}

// Delete 删除一个会话文件。
func (c *Container) Delete(id string) error {
	path, err := c.resolvePath(id)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("session: delete %s: %w", path, err)
	}
	// 旁车文件只是缓存；随转录一起删除它，
	// 这样之后复用 id 的会话就不会继承过期的元数据。
	_ = os.Remove(path + idxExt)
	return nil
}

// path 将 id 转换为文件路径，拒绝任何可能逃出 dir 的内容。
func (c *Container) path(id string) (string, error) {
	clean := strings.TrimSpace(id)
	if clean == "" {
		return "", fmt.Errorf("session: empty session id")
	}
	if strings.HasSuffix(strings.ToLower(clean), fileExt) {
		clean = clean[:len(clean)-len(fileExt)]
	}
	if clean == "" || clean == "." || clean == ".." ||
		strings.ContainsAny(clean, `/\:*?"<>|`) {
		return "", fmt.Errorf("session: invalid session id %q", id)
	}
	return filepath.Join(c.dir, clean+fileExt), nil
}

// resolvePath 接受精确的 id，否则接受唯一的、不区分大小写的前缀。
func (c *Container) resolvePath(id string) (string, error) {
	exact, err := c.path(id)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(exact); err == nil {
		return exact, nil
	}
	want := strings.ToLower(strings.TrimSuffix(filepath.Base(exact), fileExt))
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return "", fmt.Errorf("session: read %s: %w", c.dir, err)
	}
	var matches []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), fileExt) {
			continue
		}
		stem := entry.Name()[:len(entry.Name())-len(fileExt)]
		if strings.HasPrefix(strings.ToLower(stem), want) {
			matches = append(matches, filepath.Join(c.dir, entry.Name()))
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("session: no session with id %q", id)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("session: ambiguous session id %q (%d matches)", id, len(matches))
	}
}

// transcriptStem 从文件名中剥离末尾的转录扩展名，不区分大小写。
// 发现路径以不区分大小写的方式匹配 ".jsonl" 后缀（见 List 和 resolvePath），
// 因此从解析后的路径派生 id 时必须以同样方式剥离它。区分大小写的 strings.TrimSuffix
// 会把 "SESSION.JSONL" 当作 id，使 Session.path() 指向 "SESSION.JSONL.jsonl"，
// 在区分大小写的文件系统上每次读取都返回空结果。没有该后缀的名称原样返回。
func transcriptStem(name string) string {
	if len(name) >= len(fileExt) &&
		strings.EqualFold(name[len(name)-len(fileExt):], fileExt) {
		return name[:len(name)-len(fileExt)]
	}
	return name
}

// newID 生成带时间戳和随机后缀的 id 及其未使用的文件路径。
func (c *Container) newID() (string, string, error) {
	stamp := time.Now().Format("20060102-150405")
	for attempt := 0; attempt < 32; attempt++ {
		id := stamp + "-" + randomSuffix()
		path := filepath.Join(c.dir, id+fileExt)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return id, path, nil
		}
	}
	return "", "", fmt.Errorf("session: could not allocate a session id in %s", c.dir)
}

func randomSuffix() string {
	buf := make([]byte, 2)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%04x", uint16(time.Now().UnixNano()))
	}
	return hex.EncodeToString(buf)
}

// 旁车文件只是缓存，绝不是会话内容的权威来源。条目只存储 List/Search 需要的字段
// 以及转录的大小和 mtime，因此绕过缓存的追加或任何外部编辑都会被检测到，
// 转录会被重新解析。
const (
	idxExt       = ".idx"
	indexVersion = 1
)

type indexEntry struct {
	Version int   `json:"version"`
	Size    int64 `json:"size"`
	MTime   int64 `json:"mtime"`
	Meta    Meta  `json:"meta"`
}

// 只有当旁车文件可读、版本为当前版本，且描述的大小和 mtime 与转录完全一致时，
// loadIndex 才返回缓存的 meta。任何疑点都按未命中处理，交由全量扫描处理。
func loadIndex(path string, info os.FileInfo, id string) (Meta, bool) {
	data, err := os.ReadFile(path + idxExt)
	if err != nil {
		return Meta{}, false
	}
	var entry indexEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return Meta{}, false
	}
	if entry.Version != indexVersion || entry.Size != info.Size() ||
		entry.MTime != info.ModTime().UnixNano() || entry.Meta.ID != id {
		return Meta{}, false
	}
	return entry.Meta, true
}

// saveIndex 原子地刷新旁车文件，使并发读取者永远不会看到半写入的文件。
// 失败是无害的：下一次读取只会解析该文件。
func saveIndex(path string, info os.FileInfo, meta Meta) {
	data, err := json.Marshal(indexEntry{
		Version: indexVersion,
		Size:    info.Size(),
		MTime:   info.ModTime().UnixNano(),
		Meta:    meta,
	})
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".idx-*.tmp")
	if err != nil {
		return
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return
	}
	if err := os.Rename(name, path+idxExt); err != nil {
		os.Remove(name)
	}
}

// metaFromRecords 仅根据转录派生一个列表条目。
func metaFromRecords(id string, records []Record) Meta {
	meta := Meta{ID: id, Title: titleFromRecords(id, records)}
	for _, record := range records {
		if !record.TS.IsZero() {
			meta.Updated = record.TS
		}
		switch record.Kind {
		case KindAssistant:
			meta.Turns++
		case KindMessage:
			if fieldString(record.Data, "role") == "assistant" {
				meta.Turns++
			}
		}
	}
	return meta
}
