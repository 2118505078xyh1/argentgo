package security

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// PendingApproval 是一次等待操作员决定的工具调用。它是内存中审批弹窗的
// 持久化、服务端权威镜像：重新连接的客户端可以从存储中重新列出待处理请求，
// 而不会在 TUI 退出时丢失它们。原始参数从不存储；ArgsDigest 是其规范形式的
// 哈希，因此同一调用的两种写法可以比较为相等，而无需保留可能含密钥的参数文本。
type PendingApproval struct {
	ID         string `json:"id"`
	Session    string `json:"session"`
	Tool       string `json:"tool"`
	ArgsDigest string `json:"args_digest"`
	Created    string `json:"created"`
	Reason     string `json:"reason,omitempty"`
}

// 这些边界防止恶意或有缺陷的客户端用无界记录填满存储。对正常工具调用而言，
// 它们刻意设得比较宽松。
const (
	maxPendingPerSession  = 50
	maxPendingDecisionLog = 200
	maxPendingIDLen       = 256
	maxPendingSessionLen  = 512
	maxPendingToolLen     = 256
	maxPendingDigestLen   = 128
	maxPendingCreatedLen  = 64
	maxPendingReasonLen   = 4096
	maxCanonicalArgsBytes = 64 * 1024
	maxPendingLineBytes   = 2 * 1024 * 1024
)

// PendingStore 把待处理的审批持久化到磁盘。所有操作都由互斥量保护，
// 因此处理并发 HTTP/SSE 客户端的服务器可以安全地共享一个存储。零值不可用；
// 请使用 NewPendingStore。
type PendingStore struct {
	dir string
	mu  sync.Mutex

	// waiters 是进程内回答通道表。它补充（而非替代）持久化镜像：Resolve 先
	// 持久化决定，然后通过匹配的通道唤醒被阻塞的 Approver.Ask。键使用原始的
	// session/id，而不是脱敏后的磁盘值，因此通过 approval.answer 回答的服务器
	// 始终寻址到 Ask 注册的同一个等待者。
	waiters map[pendingWaiterKey]chan bool
}

// pendingWaiterKey 把一个进程内回答通道与一个待处理请求关联起来。
// Session 和 id 是调用方看到的原始值（不是哈希）。
type pendingWaiterKey struct {
	session string
	id      string
}

// NewPendingStore 返回一个以 dir 为根的存储，并创建该目录。当 dir 为空时，
// 默认为相对于工作目录的 ".argent/pending"。调用方应传入基础路径；存储只在
// 使用该默认值时追加自己的子目录。
func NewPendingStore(dir string) *PendingStore {
	if strings.TrimSpace(dir) == "" {
		dir = filepath.Join(".argent", "pending")
	}
	s := &PendingStore{dir: dir, waiters: make(map[pendingWaiterKey]chan bool)}
	// 构造函数不能返回错误；每个方法都会重试 MkdirAll，因此这里的临时
	// 文件系统故障不会致命。
	_ = os.MkdirAll(dir, 0o700)
	return s
}

// RegisterWaiter 为 (session, id) 安装一个带缓冲（容量 1）的进程内回答通道，
// 并将其与一个 cancel 函数一起返回。Resolve 以非阻塞方式把 granted 值投递到
// 通道并移除等待者，因此调用方可以在通道上 select，而不必阻塞在本地输入上。
// 调用方停止等待时必须调用 cancel（context 取消，或本地回答已胜出），以避免
// 泄漏等待者；cancel 是幂等的，可以和显式调用一起安全地 defer。对同一个键
// 注册两次会替换之前的等待者。这里支持用结构体字面量构造的存储（某些测试
// 就是这样做的）：等待者 map 会按需创建。
func (s *PendingStore) RegisterWaiter(session, id string) (<-chan bool, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.waiters == nil {
		s.waiters = make(map[pendingWaiterKey]chan bool)
	}
	key := pendingWaiterKey{session: session, id: id}
	ch := make(chan bool, 1)
	s.waiters[key] = ch
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			// 只移除我们自己的通道：后面同键的 RegisterWaiter 可能已经用新的等待者
			// 替换了它。
			if cur, ok := s.waiters[key]; ok && cur == ch {
				delete(s.waiters, key)
			}
		})
	}
	return ch, cancel
}

// deliverWaiterLocked 把 granted 交给 (session, id) 的等待者（如果存在）。
// 调用时必须已经持有 s.mu。投递是非阻塞的，并会消耗等待者，
// 因此重复的 Resolve 永远不会回答两次。
func (s *PendingStore) deliverWaiterLocked(session, id string, granted bool) {
	if len(s.waiters) == 0 {
		return
	}
	key := pendingWaiterKey{session: session, id: id}
	ch, ok := s.waiters[key]
	if !ok {
		return
	}
	delete(s.waiters, key)
	select {
	case ch <- granted:
	default:
		// 该通道容量为 1 且是新建的，因此这个分支实际上不可达；
		// 保持 Resolve 非阻塞。
	}
}

// PendingArgsDigest 返回原始参数规范形式的 sha256。调用方把该值放入
// PendingApproval.ArgsDigest；原始参数从不写入磁盘。规范化方式与
// security.canonicalArgs 一致：JSON 对象键会被排序，数字保持精确，
// 非 JSON 文本的空白会被压缩。
func PendingArgsDigest(raw string) string {
	sum := sha256.Sum256([]byte(canonicalPendingArgs(raw)))
	return hex.EncodeToString(sum[:])
}

// canonicalPendingArgs 用 encoding/json 规范化原始参数，使同一调用的两种
// 写法哈希相同。它是有界的：超长输入会被截断并附上总长度标记，这样不同的
// 长数据块不会共享同一个摘要，解码器也不必遍历无界字符串。
func canonicalPendingArgs(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "{}"
	}
	if len(raw) > maxCanonicalArgsBytes {
		raw = raw[:maxCanonicalArgsBytes] + fmt.Sprintf("\x00len=%d", len(raw))
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return strings.Join(strings.Fields(raw), " ")
	}
	// 像 json.Unmarshal 那样拒绝尾部数据，这样带后缀的值不会塌陷到
	// 裸值的摘要上。
	if err := dec.Decode(new(any)); err != io.EOF {
		return strings.Join(strings.Fields(raw), " ")
	}
	normalized, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return string(normalized)
}

// Add 存储会话的 p，替换任何相同 ID 的请求。它原子地重写整个每会话文件
// （临时文件 + 重命名），最多保留 maxPendingPerSession 条记录，超出时丢弃
// 最旧的。
func (s *PendingStore) Add(p PendingApproval) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureDir(); err != nil {
		return err
	}
	// 对原始 session 做哈希用于路径，这样文件名永远不会包含会话 id
	// （或其中任何可作路径分隔符的字符）。
	path := s.sessionPath(p.Session)
	clean := sanitizePending(p)
	items, err := s.readPending(path)
	if err != nil {
		return err
	}
	kept := make([]PendingApproval, 0, len(items)+1)
	for _, item := range items {
		if item.ID == clean.ID {
			continue
		}
		kept = append(kept, item)
	}
	kept = append(kept, clean)
	if len(kept) > maxPendingPerSession {
		kept = kept[len(kept)-maxPendingPerSession:]
	}
	return s.writePending(path, kept)
}

// List 返回 session 的待处理请求，按 Created 升序排列。session 不存在时
// 返回空（非 nil）切片。格式错误的行会被跳过，因此部分写入或手工编辑的文件
// 不会破坏重新连接。
func (s *PendingStore) List(session string) ([]PendingApproval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items, err := s.readPending(s.sessionPath(session))
	if err != nil {
		return nil, err
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Created < items[j].Created })
	if items == nil {
		items = []PendingApproval{}
	}
	return items, nil
}

// Resolve 移除给定 id 的请求，并追加一条有界的一行式决定记录。未知 id 在
// 磁盘上是空操作：重新连接的竞态可能在另一个客户端已经解决后重放某个决定，
// 若对此报错会向操作员暴露虚假错误。与磁盘结果无关，Resolve 还会用该决定
// 唤醒已注册的进程内等待者（如果有）。这正是让 approval.answer 在 --rpc 上
// 解除 pendingApprover.Ask 阻塞的机制。投递被延迟执行，因此临时的文件系统
// 错误、未知 id 或重放的决定都绝不会让被阻塞的调用方搁浅；等待者最多被消耗
// 一次，这使得重复的 Resolve 调用是安全的（幂等的）。
func (s *PendingStore) Resolve(session, id string, granted bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.deliverWaiterLocked(session, id, granted)
	if err := s.ensureDir(); err != nil {
		return err
	}
	path := s.sessionPath(session)
	items, err := s.readPending(path)
	if err != nil {
		return err
	}
	cleanID := truncatePendingRunes(Redact(id), maxPendingIDLen)
	kept := make([]PendingApproval, 0, len(items))
	found := false
	for _, item := range items {
		if item.ID == cleanID {
			found = true
			continue
		}
		kept = append(kept, item)
	}
	if !found {
		return nil
	}
	if err := s.writePending(path, kept); err != nil {
		return err
	}
	return s.appendDecision(session, cleanID, granted)
}

// Clear 移除 session 的所有待处理请求。决定历史会被保留；它是审计痕迹，
// 而不是待处理状态。
func (s *PendingStore) Clear(session string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.sessionPath(session)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("security: clear pending store: %w", err)
	}
	return nil
}

// ensureDir 以仅所有者权限创建后备目录。
func (s *PendingStore) ensureDir() error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("security: create pending store dir: %w", err)
	}
	return nil
}

// sessionHash 是会话 id 的文件系统安全、不可逆的名称。
func sessionHash(session string) string {
	sum := sha256.Sum256([]byte(session))
	return hex.EncodeToString(sum[:])
}

func (s *PendingStore) sessionPath(session string) string {
	return filepath.Join(s.dir, sessionHash(session)+".json")
}

func (s *PendingStore) decisionsPath(session string) string {
	return filepath.Join(s.dir, sessionHash(session)+".decisions.jsonl")
}

// sanitizePending 在持久化前对每个字符串字段脱敏并限制长度，使密钥和超大
// 数据块永远无法到达磁盘。缺失的 Created 会被补上，使 List 的排序有稳定的键。
func sanitizePending(p PendingApproval) PendingApproval {
	p.ID = truncatePendingRunes(Redact(p.ID), maxPendingIDLen)
	p.Session = truncatePendingRunes(Redact(p.Session), maxPendingSessionLen)
	p.Tool = truncatePendingRunes(Redact(p.Tool), maxPendingToolLen)
	p.ArgsDigest = truncatePendingRunes(p.ArgsDigest, maxPendingDigestLen)
	p.Created = truncatePendingRunes(p.Created, maxPendingCreatedLen)
	p.Reason = truncatePendingRunes(Redact(p.Reason), maxPendingReasonLen)
	if p.Created == "" {
		p.Created = time.Now().UTC().Format(time.RFC3339Nano)
	}
	return p
}

// truncatePendingRunes 把 s 限制在 max 个 rune 以内，绝不切断 UTF-8 序列。
func truncatePendingRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	n := 0
	for i := range s {
		if n == max {
			return s[:i]
		}
		n++
	}
	return s
}

// readPending 读取以换行分隔的 PendingApproval 记录，跳过空行和格式错误的行。
// 文件不存在表示空存储，而不是错误。
func (s *PendingStore) readPending(path string) ([]PendingApproval, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("security: open pending store: %w", err)
	}
	defer f.Close()
	var out []PendingApproval
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxPendingLineBytes)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var p PendingApproval
		if err := json.Unmarshal(line, &p); err != nil {
			continue
		}
		if p.ID == "" {
			continue
		}
		out = append(out, p)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("security: scan pending store: %w", err)
	}
	return out, nil
}

// writePending 通过临时文件和重命名把 path 重写为以换行分隔的 JSON，
// 沿用配置写入器的防崩溃模式。
func (s *PendingStore) writePending(path string, items []PendingApproval) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, item := range items {
		if err := enc.Encode(item); err != nil {
			return fmt.Errorf("security: encode pending store: %w", err)
		}
	}
	return atomicWriteFile(path, buf.Bytes())
}

// atomicWriteFile 以仅所有者权限把 data 写入 path。同目录下的临时文件会被
// 重命名到位，因此读取者永远看不到部分写入，崩溃也不会损坏之前的内容。
func atomicWriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("security: create pending store dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".pending-*")
	if err != nil {
		return fmt.Errorf("security: create pending temp file: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("security: chmod pending temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("security: write pending temp file: %w", err)
	}
	// 在 rename 前把数据刷到磁盘，避免掉电后目标文件为空或被截断。
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("security: sync pending temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("security: close pending temp file: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("security: rename pending temp file: %w", err)
	}
	return nil
}

// pendingDecision 是追加到 JSONL 历史中的一条已解决决定。
type pendingDecision struct {
	ID      string `json:"id"`
	Granted bool   `json:"granted"`
	TS      string `json:"ts"`
}

// appendDecision 记录一条决定，然后把日志裁剪到最后 maxPendingDecisionLog
// 行，使审计痕迹保持有界。
func (s *PendingStore) appendDecision(session, id string, granted bool) error {
	rec := pendingDecision{
		ID:      id,
		Granted: granted,
		TS:      time.Now().UTC().Format(time.RFC3339Nano),
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("security: encode pending decision: %w", err)
	}
	path := s.decisionsPath(session)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("security: open pending decisions: %w", err)
	}
	_, writeErr := f.Write(append(line, '\n'))
	closeErr := f.Close()
	if writeErr != nil {
		return fmt.Errorf("security: append pending decision: %w", writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("security: close pending decisions: %w", closeErr)
	}
	return s.boundDecisions(path)
}

// boundDecisions 在日志溢出时只保留最新的决定行。
func (s *PendingStore) boundDecisions(path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("security: read pending decisions: %w", err)
	}
	lines := bytes.Split(data, []byte("\n"))
	valid := lines[:0]
	for _, line := range lines {
		if len(bytes.TrimSpace(line)) > 0 {
			valid = append(valid, line)
		}
	}
	if len(valid) <= maxPendingDecisionLog {
		return nil
	}
	kept := valid[len(valid)-maxPendingDecisionLog:]
	out := bytes.Join(kept, []byte("\n"))
	out = append(out, '\n')
	return atomicWriteFile(path, out)
}
