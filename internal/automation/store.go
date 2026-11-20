package automation

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// storeFile 是任务列表的磁盘形态。
type storeFile struct {
	Version int    `json:"version"`
	Specs   []Spec `json:"specs"`
}

// Store 是持久任务列表：目录中的一个 JSON 文件，原子写入（临时文件 + 重命名）并由 mutex 保护，使并发
// 原子写入（临时文件 + 重命名）并由 mutex 保护，使并发
// goroutine 能安全读写它。
type Store struct {
	mu    sync.Mutex
	dir   string
	file  string
	now   func() time.Time
	specs []Spec
}

// OpenStore 加载（或创建）dir 中的任务列表。
func OpenStore(dir string) (*Store, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, errors.New("automation: the store directory is empty")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("automation: create %s: %w", dir, err)
	}
	s := &Store{dir: dir, file: filepath.Join(dir, "automations.json"), now: time.Now}
	if err := s.reloadLocked(); err != nil {
		return nil, err
	}
	return s, nil
}

// Reload 从磁盘 JSON 文件刷新内存任务列表。store 是
// Reload 从磁盘 JSON 文件刷新内存任务列表。store 是共享文件：另一个进程（例如 `automation run`）
// 可以在 daemon 运行时推进某个时隙或添加任务。重载让
// 可以在 daemon 运行时推进某个时隙或添加任务。重载让 daemon 观察到这些变化，而不是根据陈旧的缓存视图行动，
// 后者会再次触发一个已处理的时隙。
func (s *Store) Reload() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reloadLocked()
}

// reloadLocked 把 JSON 文件解析进 s.specs。调用方必须持有 s.mu。
// 文件缺失表示空任务列表（新建的 store）。
func (s *Store) reloadLocked() error {
	data, err := os.ReadFile(s.file)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		s.specs = nil
		return nil
	case err != nil:
		return fmt.Errorf("automation: read %s: %w", s.file, err)
	}
	var file storeFile
	if err := json.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("automation: %s is not a valid task list: %w", s.file, err)
	}
	var specs []Spec
	for _, spec := range file.Specs {
		if spec.ID == "" {
			spec.ID = slug(spec.Name)
		}
		if err := spec.Validate(); err != nil {
			return err
		}
		specs = append(specs, spec)
	}
	s.specs = specs
	return nil
}

// Dir 返回 store 目录。
func (s *Store) Dir() string { return s.dir }

// File 返回 JSON 任务列表的路径。
func (s *Store) File() string { return s.file }

// SetClock 覆盖用于种入下次运行时间的时钟。
func (s *Store) SetClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if now != nil {
		s.now = now
	}
}

// List 按插入顺序返回所有任务的副本。
func (s *Store) List() []Spec {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Spec, len(s.specs))
	copy(out, s.specs)
	return out
}

// Get 返回一个任务。
func (s *Store) Get(id string) (Spec, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.indexLocked(id)
	if idx < 0 {
		return Spec{}, false
	}
	return s.specs[idx], true
}

// Put 验证并更新（upsert）一个任务，然后持久化列表。没有下次运行时间的
// Put 验证并更新（upsert）一个任务，然后持久化列表。没有下次运行时间的任务会用 store 时钟的下一个时隙种入。
func (s *Store) Put(spec Spec) error {
	if strings.TrimSpace(spec.ID) == "" {
		spec.ID = slug(spec.Name)
	}
	if err := spec.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if idx := s.indexLocked(spec.ID); idx >= 0 {
		// 调用方只提供定义时保留运行历史。
		old := s.specs[idx]
		if spec.LastRun == nil {
			spec.LastRun = old.LastRun
		}
		if spec.LastStatus == "" {
			spec.LastStatus = old.LastStatus
		}
		if spec.LastLog == "" {
			spec.LastLog = old.LastLog
		}
		if spec.NextDue.IsZero() {
			if old.Schedule == spec.Schedule && !old.NextDue.IsZero() {
				spec.NextDue = old.NextDue
			} else {
				base := s.now()
				if spec.LastRun != nil {
					base = *spec.LastRun
				}
				spec.NextDue = spec.Schedule.Next(base)
			}
		}
		s.specs[idx] = spec
	} else {
		if spec.NextDue.IsZero() {
			spec.NextDue = spec.Schedule.Next(s.now())
		}
		s.specs = append(s.specs, spec)
	}
	return s.saveLocked()
}

// Delete 删除一个任务并持久化列表。
func (s *Store) Delete(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.indexLocked(id)
	if idx < 0 {
		return false, nil
	}
	s.specs = append(s.specs[:idx], s.specs[idx+1:]...)
	return true, s.saveLocked()
}

// SetEnabled 打开或关闭任务。
func (s *Store) SetEnabled(id string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.indexLocked(id)
	if idx < 0 {
		return fmt.Errorf("automation: no task %q", id)
	}
	s.specs[idx].Enabled = enabled
	if enabled && s.specs[idx].NextDue.IsZero() {
		s.specs[idx].NextDue = s.specs[idx].Schedule.Next(s.now())
	}
	return s.saveLocked()
}

// Due 返回下次时隙已过的启用任务。
func (s *Store) Due(now time.Time) []Spec {
	s.mu.Lock()
	defer s.mu.Unlock()
	var due []Spec
	for _, spec := range s.specs {
		if spec.IsDue(now) {
			due = append(due, spec)
		}
	}
	return due
}

// RecordRun 存储一次运行的结果并把下一时隙推进到 at 之后。
// 手动运行没有计划时隙，因此调度从 at 恢复。
func (s *Store) RecordRun(id, status, logPath string, at time.Time) error {
	return s.recordRun(id, status, logPath, at, time.Time{})
}

// recordRun 是带触发运行的时隙的 RecordRun。当时隙已知时，下一次触发从该时隙而非从（可能晚得多的）完成时间推进，
// 已知时，下一次触发从该时隙而非从（可能
// 晚得多的）完成时间推进，使慢任务不会把调度表
// 使慢任务不会把调度表向前推。at 仍是记录为 LastRun 的时间。
func (s *Store) recordRun(id, status, logPath string, at, slot time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// 一次运行可能比一次扫描活得更久，而任务列表是共享 JSON 文件。运行进行期间另一个进程可能添加、编辑或删除
	// 运行进行期间另一个进程可能添加、编辑或删除
	// 任务。从扫描时取得的内存快照保存结果
	// 会破坏这些编辑（并复活运行中删除的任务），因为
	// 任务。从扫描时取得的内存快照保存结果会破坏这些编辑（并复活运行中删除的任务），因为 saveLocked 重写整个列表。
	// 持锁，使这成为对当前文件的读-改-写。同时被删除的任务
	// 只是从列表中消失，调用方已将其视为
	// "无可记录"。
	if err := s.reloadLocked(); err != nil {
		return err
	}
	idx := s.indexLocked(id)
	if idx < 0 {
		return fmt.Errorf("automation: no task %q", id)
	}
	run := at
	s.specs[idx].LastRun = &run
	s.specs[idx].LastStatus = status
	s.specs[idx].LastLog = logPath
	base := slot
	if base.IsZero() {
		base = at
	}
	next := s.specs[idx].Schedule.Next(base)
	if !next.IsZero() && !next.After(at) {
		// 持锁时从磁盘刷新，使这成为对当前文件的读-改-写。同时被删除的任务只是从列表中消失，调用方已将其视为
		// "无可记录"。
		// 我们刚推进到的时隙已在过去（运行开始晚或持续时间超过间隔）。从运行时间恢复，使 run_once 任务不会在下一个 tick 再次触发。
		next = s.specs[idx].Schedule.Next(at)
	}
	s.specs[idx].NextDue = next
	return s.saveLocked()
}

// Save 重写任务列表。
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

func (s *Store) indexLocked(id string) int {
	for i, spec := range s.specs {
		if spec.ID == id {
			return i
		}
	}
	return -1
}

func (s *Store) saveLocked() error {
	data, err := json.MarshalIndent(storeFile{Version: 1, Specs: s.specs}, "", "  ")
	if err != nil {
		return fmt.Errorf("automation: encode %s: %w", s.file, err)
	}
	data = append(data, '\n')
	if err := writeAtomic(s.file, data); err != nil {
		return fmt.Errorf("automation: write %s: %w", s.file, err)
	}
	return nil
}

// writeAtomic 把数据写入目标目录中的临时文件并重命名到位，使读取者永不会看到半写的文件。
// 重命名到位，使读取者永不会看到半写的文件。
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}
