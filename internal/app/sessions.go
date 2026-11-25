package app

import (
	"fmt"
	"path/filepath"
	"strings"

	"example.com/argentgo/internal/agent"
	"example.com/argentgo/internal/cli"
	"example.com/argentgo/internal/server"
	"example.com/argentgo/internal/session"
)

// sessionStatusRegistry 是进程级执行状态注册表：Sessions 返回的 store 与 Execute
// 共用同一个实例（TODO #28）。零值可用；并发安全由 agent.SessionStatusRegistry 保证。
var sessionStatusRegistry agent.SessionStatusRegistry

// 编译期断言：会话存储适配器实现 server 的可选状态读取端与会话 CRUD 端。
var (
	_ server.SessionStatusStore = (*sessionStore)(nil)
	_ server.SessionCreateStore = (*sessionStore)(nil)
	_ server.SessionRenameStore = (*sessionStore)(nil)
	_ server.SessionDeleteStore = (*sessionStore)(nil)
)

// NewSession 实现 server.SessionCreateStore：立即在存储目录创建只含 header 的
// 会话并返回 id，使 GUI/TUI 的"新建"能拿到可激活的真实会话而非仅清屏。
func (s *sessionStore) NewSession(title string) (string, error) {
	store, err := session.Open(s.dir)
	if err != nil {
		return "", err
	}
	sess, err := store.NewSession(title)
	if err != nil {
		return "", err
	}
	return sess.ID(), nil
}

// RenameSession 实现 server.SessionRenameStore：追加一条标题覆盖记录。
func (s *sessionStore) RenameSession(id, title string) error {
	sess, err := session.OpenSession(s.dir, id)
	if err != nil {
		return err
	}
	return sess.SetTitle(title)
}

// openOrCreateSession 实现 --session/--resume：显式的 id 或会话文件会被重新打开，
// --resume 选择最新的已存储会话，只有普通运行才会创建新会话。返回的句柄会被原地追加。
// 显式 --session 同时带 --at 时，通过 OpenSessionAt 就地 checkout 到指定记录，
// 使后续消息从该分叉点续写；不带 --at 时走原来的 OpenSession，行为逐字节不变。
func openOrCreateSession(store *session.Store, opts cli.Options, firstPrompt string) (*session.Session, error) {
	if id := strings.TrimSpace(opts.Session); id != "" {
		resolvedID := sessionIDFromArg(store, id)
		if at := strings.TrimSpace(opts.At); at != "" {
			sess, err := store.OpenSessionAt(resolvedID, at)
			if err != nil {
				return nil, fmt.Errorf("resume session %q at %q: %w", id, at, err)
			}
			return sess, nil
		}
		sess, err := store.OpenSession(resolvedID)
		if err != nil {
			return nil, fmt.Errorf("resume session %q: %w", id, err)
		}
		return sess, nil
	}
	if opts.Resume {
		metas, err := store.List()
		if err != nil {
			return nil, fmt.Errorf("list sessions for --resume: %w", err)
		}
		if len(metas) > 0 {
			sess, err := store.OpenSession(metas[0].ID)
			if err != nil {
				return nil, fmt.Errorf("resume session %q: %w", metas[0].ID, err)
			}
			return sess, nil
		}
	}
	title := session.TitleFromPrompt(firstPrompt)
	if title == "" {
		title = "session"
	}
	sess, err := store.NewSession(title)
	if err != nil {
		return nil, fmt.Errorf("new session: %w", err)
	}
	// 即使在全新会话上，显式的 --title 也优先于从提示派生的标题；
	// cli.applySessionFlags 只有在 id 存在后才能应用它。
	if override := strings.TrimSpace(opts.Title); override != "" {
		if err := cli.ApplyTitleToSession(sess, override); err != nil {
			return nil, fmt.Errorf("set session title: %w", err)
		}
	}
	return sess, nil
}

// sessionIDFromArg 接受裸 id（或唯一前缀）以及指向会话存储内文件的路径。
// 存储之外的路径会被拒绝，使精心构造的 --session 值不能指向任意文件。
func sessionIDFromArg(store *session.Store, value string) string {
	separator := strings.ContainsAny(value, `/\`)
	if !separator && !strings.HasSuffix(strings.ToLower(value), ".jsonl") {
		return value
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return value
	}
	dir := filepath.Clean(store.Dir())
	if filepath.Clean(filepath.Dir(absolute)) != dir {
		return value // OpenSession 会拒绝路径中的分隔符。
	}
	return strings.TrimSuffix(filepath.Base(absolute), filepath.Ext(absolute))
}

// Sessions 返回供 `argentgo sessions` 和 RPC/ACP 传输使用的会话存储适配器。
// 它持有进程级的执行状态注册表，使 session.list 能报告哪个会话正在工作。
func Sessions(storage string) cli.SessionStore {
	return &sessionStore{dir: filepath.Join(storage, "sessions"), statuses: &sessionStatusRegistry}
}

type sessionStore struct {
	dir      string
	statuses *agent.SessionStatusRegistry
}

func (s *sessionStore) List() ([]server.SessionInfo, error) {
	metas, err := session.List(s.dir)
	if err != nil {
		return nil, err
	}
	statuses := s.Snapshot()
	out := make([]server.SessionInfo, 0, len(metas))
	for _, meta := range metas {
		out = append(out, server.SessionInfo{
			ID:      meta.ID,
			Title:   meta.Title,
			Updated: meta.Updated,
			Status:  statuses[meta.ID],
		})
	}
	return out, nil
}

// Snapshot 实现 server.SessionStatusStore，把 agent 的状态类型投影为 wire 字符串。
// 未记录（或注册表未注入）的会话返回空串，由 SessionInfo 的 omitempty 省略。
func (s *sessionStore) Snapshot() map[string]string {
	out := map[string]string{}
	if s == nil || s.statuses == nil {
		return out
	}
	for id, status := range s.statuses.Snapshot() {
		out[id] = string(status)
	}
	return out
}

func (s *sessionStore) Load(id string) ([]server.SessionEvent, error) {
	sess, err := session.OpenSession(s.dir, id)
	if err != nil {
		return nil, err
	}
	records, err := sess.Records()
	if err != nil {
		return nil, err
	}
	events := make([]server.SessionEvent, 0, len(records))
	for _, record := range records {
		events = append(events, server.SessionEvent{
			ID:     record.ID,
			Parent: record.Parent,
			Kind:   record.Kind,
			TS:     record.TS,
			Data:   record.Data,
		})
	}
	return events, nil
}

func (s *sessionStore) Delete(id string) error {
	if err := session.Delete(s.dir, id); err != nil {
		return fmt.Errorf("delete session %s: %w", id, err)
	}
	return nil
}

// Branches 实现 server.SessionTreeStore，把会话分支映射为 RPC wire 类型。
// 显式 schema 头是元数据，不属于对话树，过滤掉以免在 GUI 中多出一层节点。
// Fork 由"同一 parent 下是否有多个子节点"派生：真分叉点上的每个子节点
// 都标记为 Fork，使客户端可以高亮所有分叉路径。
func (s *sessionStore) Branches(id string) ([]server.SessionBranch, error) {
	path, err := session.ResolvePath(s.dir, id)
	if err != nil {
		return nil, err
	}
	branches, err := session.Branches(path)
	if err != nil {
		return nil, err
	}
	// 统计每个 parent 的子节点数，用于派生 Fork 标记。
	children := make(map[string]int, len(branches))
	for _, branch := range branches {
		if branch.Kind == session.KindSchema {
			continue
		}
		children[branch.Parent]++
	}
	out := make([]server.SessionBranch, 0, len(branches))
	for _, branch := range branches {
		if branch.Kind == session.KindSchema {
			continue
		}
		out = append(out, server.SessionBranch{
			ID:     branch.ID,
			Parent: branch.Parent,
			Kind:   branch.Kind,
			Depth:  branch.Depth,
			Active: branch.Active,
			Label:  branch.Label,
			Fork:   children[branch.Parent] > 1,
		})
	}
	return out, nil
}
