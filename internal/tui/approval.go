package tui

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"
)

// approvalGrantKey 把会话授权限定到某个工具及其详情。NUL
// 分隔符防止 name/detail 组合与纯 name 键冲突。
func approvalGrantKey(name, args string) string {
	return name + "\x00" + approvalDetail(args)
}

// approvalDetail 提取标识一次调用的参数。JSON 调用使用
// 第一个 path/command 字段（清洗后），因此用 `ls` 授权 `bash` 不能
// 悄悄授权带 `sudo` 的 `bash`。旧调用方使用的纯字符串参数
// 回退到第一个 token；这让 "ls" 和 "ls -la" 共用一次授权，
// 与这些输入历史上的粗粒度行为一致。
func approvalDetail(args string) string {
	trimmed := strings.TrimSpace(args)
	if strings.HasPrefix(trimmed, "{") {
		var decoded map[string]any
		if err := json.Unmarshal([]byte(trimmed), &decoded); err == nil {
			for _, key := range []string{"command", "cmd", "path", "file", "filepath", "query", "pattern", "url", "uri", "name", "job"} {
				value, ok := decoded[key].(string)
				if !ok || strings.TrimSpace(value) == "" {
					continue
				}
				switch key {
				case "path", "file", "filepath":
					return filepath.Clean(strings.TrimSpace(value))
				default:
					return collapseSpace(value)
				}
			}
		}
	}
	if fields := strings.Fields(trimmed); len(fields) > 0 {
		return fields[0]
	}
	return ""
}

// approvalRequest 是等待操作员处理的一次工具调用。
type approvalRequest struct {
	name string
	args string
	// danger 是人可读的原因，说明该调用为何被标记为危险。
	// 空值（默认）使历史弹窗逐字节保持不变。
	// 设置后，弹窗显示原因并预选 Deny 作为安全的
	// 默认值，使习惯性的 Enter 不会批准破坏性命令。
	danger string
	// intent 是危险命令的一行中文意图摘要，渲染在裸参数上方，让操作员
	// 先看到"它要做什么"。普通请求为空，整段跳过，帧字节保持历史原样。
	intent string
	// diff 是审批 diff 数据（默认渲染）。零值时跳过摘要与预览行；
	// 仅当 ARGENT_APPROVAL_DIFF=0 紧急关闭开关时即便有数据也不渲染。
	diff  ApprovalDiff
	reply chan bool
	ctx   context.Context
	// openedAt 记录对话框真正显示给操作者的时刻，供可选的按键宽限期判断。
	// 排队等待的请求在被提升为当前对话框时会重新打戳，因此这里衡量的是
	// “弹窗出现”而非“请求创建”的时间。零值与关闭宽限期时完全等价。
	openedAt time.Time
}

// defaultsToDeny 报告此请求是否预选 Deny。普通
// 请求保留 Allow 作为主要操作项，并有意保持
// 不变；只有被标记的请求才翻转安全默认值。
func (r *approvalRequest) defaultsToDeny() bool {
	return r != nil && r.danger != ""
}

// dangerIntent 把危险原因折叠成一行中文意图摘要，渲染在裸参数上方。
// 普通请求（danger 为空）返回空串，调用方据此跳过整段渲染。
func dangerIntent(danger string) string {
	if strings.TrimSpace(danger) == "" {
		return ""
	}
	return "危险命令意图：" + strings.TrimSpace(danger)
}

func (m *Model) answerApprovalLocked(key Key) KeyAction {
	request := m.pending
	if request == nil {
		return ActionNone
	}
	switch key.Kind {
	case KindRune:
		switch key.Rune {
		case 'y', 'Y':
			m.finishApprovalLocked(true, text(m.locale, "approved"))
		case 'n', 'N':
			m.finishApprovalLocked(false, text(m.locale, "denied"))
		case 'a', 'A':
			// 只授权这次精确调用：粗粒度的 name 键会允许之后
			// 用不同参数对同一工具的每次调用。
			m.allowed[approvalGrantKey(request.name, request.args)] = true
			m.finishApprovalLocked(true, text(m.locale, "approved_session"))
		}
	case KindEsc, KindCtrlC:
		m.finishApprovalLocked(false, text(m.locale, "denied"))
	case KindEnter:
		// 被标记的请求预选 Deny：含义模糊的确认键绝不能
		// 批准破坏性命令。非危险请求保持其
		// 历史空操作，因此行为不变。
		if request.defaultsToDeny() {
			m.finishApprovalLocked(false, text(m.locale, "denied"))
		}
	}
	return ActionNone
}

func (m *Model) finishApprovalLocked(ok bool, status string) {
	request := m.pending
	if request == nil {
		return
	}
	m.pending = nil
	select {
	case request.reply <- ok:
	default:
	}
	m.setStatusLocked(status)
	m.promoteApprovalLocked()
}

func (m *Model) promoteApprovalLocked() {
	for m.pending == nil && len(m.approvalQueue) > 0 {
		next := m.approvalQueue[0]
		m.approvalQueue = m.approvalQueue[1:]
		if next.ctx != nil && next.ctx.Err() != nil {
			continue
		}
		// 排队请求此刻才真正出现在屏幕上，重新打戳让宽限期从弹窗时刻算起。
		next.openedAt = m.now()
		m.pending = &next
	}
}

func (m *Model) dropApprovalLocked(reply chan bool) {
	if m.pending != nil && m.pending.reply == reply {
		m.pending = nil
		m.promoteApprovalLocked()
		return
	}
	for i := range m.approvalQueue {
		if m.approvalQueue[i].reply == reply {
			m.approvalQueue = append(m.approvalQueue[:i], m.approvalQueue[i+1:]...)
			return
		}
	}
}

// Approve 请求操作员允许一次工具调用，并阻塞直到弹窗
// 被回答或 ctx 结束。安装时，答案通过注入的
// Options.Approval 权限返回。这是普通的、
// 非危险入口点：弹窗保持其历史 Allow 默认值。
func (m *Model) Approve(ctx context.Context, name, args string) (bool, error) {
	return m.approve(ctx, name, args, "", ApprovalDiff{})
}

// ApproveDanger 是危险命令的入口点。非空的 danger 原因
// 会显示在弹窗中，并把默认答案翻转为 Deny。app 层
// （它拥有 security.FindFileEditingCommand）负责提供该原因；
// TUI 包不导入 security。danger 为空时，其行为
// 与 Approve 完全一致。
func (m *Model) ApproveDanger(ctx context.Context, name, args, danger string) (bool, error) {
	return m.approve(ctx, name, args, danger, ApprovalDiff{})
}

// ApproveDiff 与普通 Approve 类似，但附带编辑前后的文件快照，供审批卡
// 默认渲染真实 diff。diff 由 BuildApprovalDiff 从 before/after 构造；
// 快照只读、不修改入参。默认开启，仅当 ARGENT_APPROVAL_DIFF=0 紧急关闭时
// 即便传入了数据，弹窗也与普通 Approve 逐字节相同。danger 语义与
// ApproveDanger 一致，可同时传入。
func (m *Model) ApproveDiff(ctx context.Context, name, args, danger string, before, after map[string]string) (bool, error) {
	return m.approve(ctx, name, args, danger, BuildApprovalDiff(before, after))
}

func (m *Model) approve(ctx context.Context, name, args, danger string, diff ApprovalDiff) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	m.mu.Lock()
	// 纯 name 键仍被遵守，使旧调用方创建的授权
	// （它们直接缓存 m.allowed）继续工作。新授权是复合的。
	if m.allowed[name] || m.allowed[approvalGrantKey(name, args)] {
		m.mu.Unlock()
		return true, nil
	}
	request := approvalRequest{name: name, args: args, danger: danger, intent: dangerIntent(danger), diff: diff, reply: make(chan bool, 1), ctx: ctx}
	m.appendTaskLocked(Task{Name: name, Args: args, Status: TaskPending})
	if m.pending == nil {
		request.openedAt = m.now()
		m.pending = &request
	} else {
		m.approvalQueue = append(m.approvalQueue, request)
	}
	m.mu.Unlock()

	var ok bool
	select {
	case ok = <-request.reply:
	case <-ctx.Done():
		m.mu.Lock()
		m.dropApprovalLocked(request.reply)
		m.mu.Unlock()
		return false, ctx.Err()
	}
	if !ok {
		return false, nil
	}
	m.mu.Lock()
	authority := m.approvalFn
	m.mu.Unlock()
	if authority != nil {
		return authority(ctx, name, args)
	}
	return true, nil
}

// PendingApproval 报告弹窗正在显示的工具调用。
func (m *Model) PendingApproval() (name, args string, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending == nil {
		return "", "", false
	}
	return m.pending.name, m.pending.args, true
}

// Tick 丢弃上下文已消失的授权；循环每帧调用它。
func (m *Model) Tick() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending != nil && m.pending.ctx != nil && m.pending.ctx.Err() != nil {
		m.pending = nil
		m.promoteApprovalLocked()
	}
}
