package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/security"
)

// panicLogf 是各传输的 fire-and-forget goroutine 捕获 panic 时的日志出口。
// 声明为变量以便测试注入捕获；默认走标准库 log，与 internal/config、
// internal/auth 的告警日志风格一致。只有 panic 路径才会调用它。
var panicLogf = log.Printf

// logRecoveredPanic 记录一次被恢复的 panic：包含 panic 值与调用栈，
// 便于事后定位是哪条 goroutine、哪段代码触发。栈信息只写服务端日志，
// 不跨越传输边界，客户端只收到统一的内部错误回包。
func logRecoveredPanic(where string, r any) {
	panicLogf("server: recovered panic in %s: %v\n%s", where, r, debug.Stack())
}

// maxRPCFrame 是换行分隔的 --rpc / acp 传输上接受的最大帧。
// 它把旧 bufio.Scanner 的 4 MiB 上限作为显式的、
// 强制执行的限制保留下来。更长的帧会被结构化的 JSON-RPC 错误
// 拒绝并丢弃；传输继续读取下一帧。
const maxRPCFrame = 4 * 1024 * 1024

// errFrameTooLong 标记一个被丢弃的超大帧。它有意不是
// 终态传输错误：scanLines 已经消费了整帧，
// 之后会恢复正常读取。
var errFrameTooLong = fmt.Errorf("json-rpc frame too long (max %d bytes)", maxRPCFrame)

// rpcAbsPath 匹配嵌在会话层错误中的绝对文件系统路径。
// Windows 盘符根（C:\...）和 POSIX 根（/...）都覆盖；
// 保留前导分隔符，使周围的文字仍然可读。
// 尾部字符类在空白及常见的引号/冒号分隔符处停止，
// 从而保留路径后面的错误原因。
var rpcAbsPath = regexp.MustCompile(`(^|[\s"'=(:;])((?:[A-Za-z]:[\\/])|/)[^\s"'<>|:]*`)

// scrubAbsolutePaths 折叠字符串中的绝对文件系统路径，使其不跨越 RPC 边界。
// 凭据形态的片段先经 security.Redact 脱敏，绝对路径（Windows 盘符根与 POSIX
// 根）再统一替换为 <storage>/... 占位符。session.load / session.tree 的错误
// 与 config.info 的 workspace 共用这一条边界，避免两处规则漂移。
func scrubAbsolutePaths(s string) string {
	return rpcAbsPath.ReplaceAllString(security.Redact(s), "${1}<storage>/...")
}

// scrubSessionLoadError 为 JSON-RPC 回复准备 session.load 失败信息。
// 会话层错误会插入绝对存储目录（例如
// C:\Users\<user>\.argentgo\sessions\<id>.jsonl），它不得跨越 RPC
// 边界。凭据形态的片段会被脱敏，绝对路径会被折叠
// 为占位符；规范原因（以及调用方提供的 session id）
// 保留下来，使客户端仍能知道加载失败的原因。未脱敏的细节
// 留在服务器端用于日志。
func scrubSessionLoadError(id string, err error) string {
	return fmt.Sprintf("load session %q: %s", id, scrubAbsolutePaths(err.Error()))
}

// filterEventsByBranch 把 session.load 返回的全部事件裁剪为根→branchID 的
// 祖先链。branchID 必须对应某条事件的 ID；未知分支返回 ok=false，由调用方
// 转成干净的 RPC 错误（与 session/tree.go 拒绝未知分支的语义一致）。遍历
// 沿 parent 回溯，用 seen 防环，过滤后保持 events 原有顺序。
func filterEventsByBranch(events []SessionEvent, branchID string) ([]SessionEvent, bool) {
	byID := make(map[string]SessionEvent, len(events))
	for _, ev := range events {
		if ev.ID != "" {
			byID[ev.ID] = ev
		}
	}
	if _, ok := byID[branchID]; !ok {
		return nil, false
	}
	keep := make(map[string]bool, len(events))
	seen := make(map[string]bool, len(events))
	for current := branchID; current != ""; {
		if seen[current] {
			break
		}
		ev, ok := byID[current]
		if !ok {
			break
		}
		seen[current] = true
		keep[current] = true
		current = ev.Parent
	}
	filtered := make([]SessionEvent, 0, len(keep))
	for _, ev := range events {
		if keep[ev.ID] {
			filtered = append(filtered, ev)
		}
	}
	return filtered, true
}

// scanResult 是 scanLines 产出的一项：裁剪后的非空输入行，或
// 一个错误。errFrameTooLong 表示一个超大帧被丢弃，
// 读取继续；任何其他错误都是终态的（in 上真正的 I/O 失败）。
type scanResult struct {
	line []byte
	err  error
}

// scanLines 在后台 goroutine 上读取换行分隔的输入，使传输
// 即使 stdin 没有数据就绪也能响应 ctx 取消——
// 裸的阻塞 Read 会阻塞传输自身的 goroutine 并吞掉
// Ctrl-C。
//
// 它使用 bufio.Reader 而不是 bufio.Scanner，因为 Scanner 无法从
// bufio.ErrTooLong 中恢复：一旦某行超出其缓冲区，它就永久停止，
// 过去这会因为一个坏帧而拆除整个 RPC/ACP 连接。
// 这里超大行会被排空（不缓冲）并通过
// errFrameTooLong 报告，之后正常扫描恢复。累积的字节会被
// 复制，因为 ReadSlice 返回的切片只在下次读取前有效。
func scanLines(ctx context.Context, in io.Reader) <-chan scanResult {
	results := make(chan scanResult)
	go func() {
		defer close(results)
		br := bufio.NewReaderSize(in, 64*1024)
		line := make([]byte, 0, 64*1024)
		tooLong := false
		send := func(res scanResult) bool {
			select {
			case results <- res:
				return true
			case <-ctx.Done():
				return false
			}
		}
		for {
			chunk, err := br.ReadSlice('\n')
			if err != bufio.ErrBufferFull && len(chunk) > 0 && chunk[len(chunk)-1] == '\n' {
				chunk = chunk[:len(chunk)-1] // 从限制中排除分隔符
			}
			if !tooLong {
				if len(line)+len(chunk) > maxRPCFrame {
					tooLong = true
					line = line[:0] // 停止缓冲该帧的其余部分
				} else {
					line = append(line, chunk...)
				}
			}
			if err == bufio.ErrBufferFull {
				continue // 该帧在下一个分块中继续
			}
			if err != nil && err != io.EOF {
				send(scanResult{err: err}) // terminal I/O error
				return
			}
			if tooLong {
				if !send(scanResult{err: errFrameTooLong}) {
					return
				}
			} else if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
				if !send(scanResult{line: append([]byte(nil), trimmed...)}) {
					return
				}
			}
			line = line[:0]
			tooLong = false
			if err == io.EOF {
				return
			}
		}
	}()
	return results
}

// approvalSession 解析审批请求所针对的会话。
// 像原生桥这样的客户端只发送 {id, granted} 而不带 session，
// 而持久的待处理记录和被阻塞审批者的等待者，
// 都以 runner 当前正在追加的会话为键。没有该回退，
// 空会话会悄悄解析不出任何东西：RPC 仍回答 ok，但 Ask 永不唤醒。
// 通过一个小接口发现 SessionID，使假实现和独立调用方
// 保持原有的显式会话行为。
func approvalSession(explicit string, runner Runner) string {
	if explicit != "" {
		return explicit
	}
	if typed, ok := runner.(interface{ SessionID() string }); ok {
		return typed.SessionID()
	}
	return ""
}

// reportScanError 把可恢复的扫描错误转换为结构化的 JSON-RPC
// 回复。它报告错误是否是终态的、传输是否必须停止。
// 超大帧没有可解析的 id，因此用 id: null 回复，
// 与普通解析错误的形态相同。
func reportScanError(p *peer, err error) (terminal bool) {
	if !errors.Is(err, errFrameTooLong) {
		return true
	}
	p.fail(json.RawMessage("null"), codeParseError, "parse error: "+err.Error())
	return false
}

// RunRPC 在 in/out 上提供换行分隔的 JSON-RPC 2.0 服务，直到 in 到达
// EOF 或 ctx 被取消。
//
// 方法：prompt、abort、session.list、session.load、session.tree、
// config.info、approval.list、approval.answer、event/subscribe。流式
// 提示文本在提示结果返回前作为 prompt/delta 通知发出；
// event/subscribe 用订阅时捕获的 seq 回复（安全续传游标），
// 然后发出 "event" 通知，直到传输
// 结束。
func RunRPC(ctx context.Context, in io.Reader, out io.Writer, cfg Config) error {
	ctx, cancel := context.WithCancel(ctx)

	hub := selectHub(cfg)

	p := newPeer(out)
	var wg sync.WaitGroup
	defer func() {
		cancel()
		wg.Wait()
	}()

	results := scanLines(ctx, in)
	for {
		// 优先处理取消，使排队的行不会延迟退出。
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		select {
		case <-ctx.Done():
			return nil
		case res, ok := <-results:
			if !ok { // EOF
				return nil
			}
			if res.err != nil {
				if reportScanError(p, res.err) {
					return res.err
				}
				continue
			}
			var req request
			if err := json.Unmarshal(res.line, &req); err != nil {
				p.fail(json.RawMessage("null"), codeParseError, "parse error: "+err.Error())
				continue
			}
			if req.Method == "" {
				p.fail(req.ID, codeInvalidRequest, "missing method")
				continue
			}
			handleRPCRequest(ctx, p, cfg, req, &wg, hub)
		}
	}
}

func handleRPCRequest(ctx context.Context, p *peer, cfg Config, req request, wg *sync.WaitGroup, hub *EventHub) {
	switch req.Method {
	case "prompt":
		text := promptText(req.Params)
		if text == "" {
			p.fail(req.ID, codeInvalidParams, "prompt has no text")
			return
		}
		if cfg.Runner == nil {
			p.fail(req.ID, codeServerError, "no runner configured")
			return
		}
		id := req.ID
		wg.Add(1)
		go func() {
			defer wg.Done()
			// 兜底恢复：Runner 或流式回调中的 panic 不得终结整个 --rpc 进程。
			// 本 defer 在 wg.Done 之后注册，故先于 wg.Done 执行（后进先出），
			// 保证错误回包在 wg 归零、RunRPC 返回之前写出，客户端不会挂起。
			defer func() {
				if r := recover(); r != nil {
					logRecoveredPanic("rpc prompt", r)
					// 复用真实失败同款 -32603 回包，保持 wire 形态一致。
					p.fail(id, codeInternalError, "internal error: prompt panicked")
				}
			}()
			result, err := cfg.Runner.Prompt(ctx, text, func(chunk string) {
				if chunk == "" {
					return
				}
				p.notify("prompt/delta", map[string]any{"text": chunk})
			})
			if err != nil {
				if isCancelled(ctx, err) {
					// 用户中止是正常的停止，不是内部错误。保留
					// 结果形态的回复（带部分答案），使客户端
					// 能把它与真正的 -32603 失败区分开。
					p.respond(id, map[string]any{"text": result, "stopReason": "cancelled"})
					return
				}
				p.fail(id, codeInternalError, wireErrorText(err))
				return
			}
			p.respond(id, map[string]any{"text": result})
		}()
	case "abort":
		if cfg.Runner == nil {
			p.fail(req.ID, codeServerError, "no runner configured")
			return
		}
		cfg.Runner.Abort()
		p.respond(req.ID, map[string]any{"ok": true})
	case "session.list":
		if cfg.Sessions == nil {
			p.fail(req.ID, codeServerError, "session store not configured")
			return
		}
		infos, err := cfg.Sessions.List()
		if err != nil {
			p.fail(req.ID, codeInternalError, err.Error())
			return
		}
		if infos == nil {
			infos = []SessionInfo{}
		}
		p.respond(req.ID, map[string]any{"sessions": infos})
	case "session.load":
		if cfg.Sessions == nil {
			p.fail(req.ID, codeServerError, "session store not configured")
			return
		}
		var params struct {
			ID        string `json:"id"`
			SessionID string `json:"sessionId"`
			Branch    string `json:"branch"`
		}
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &params); err != nil {
				p.fail(req.ID, codeInvalidParams, "invalid params: "+err.Error())
				return
			}
		}
		id := params.ID
		if id == "" {
			id = params.SessionID
		}
		if id == "" {
			p.fail(req.ID, codeInvalidParams, "session id is required")
			return
		}
		events, err := cfg.Sessions.Load(id)
		if err != nil {
			p.fail(req.ID, codeServerError, scrubSessionLoadError(id, err))
			return
		}
		if params.Branch != "" {
			filtered, ok := filterEventsByBranch(events, params.Branch)
			if !ok {
				p.fail(req.ID, codeServerError, scrubSessionLoadError(id, fmt.Errorf("unknown branch %q", params.Branch)))
				return
			}
			events = filtered
		}
		if events == nil {
			events = []SessionEvent{}
		}
		p.respond(req.ID, map[string]any{"id": id, "events": events})
	case "session.tree":
		// session.tree {id} 返回会话的 id/parent 分支树。存储通过可选
		// 接口发现：没有实现它的假实现和独立调用方会得到干净的
		// "not configured" 错误，而不是 panic。错误复用 session.load
		// 的脱敏路径，避免绝对存储目录跨越 RPC 边界。
		if cfg.Sessions == nil {
			p.fail(req.ID, codeServerError, "session store not configured")
			return
		}
		typed, ok := cfg.Sessions.(SessionTreeStore)
		if !ok {
			p.fail(req.ID, codeServerError, "session tree not configured")
			return
		}
		var params struct {
			ID string `json:"id"`
		}
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &params); err != nil {
				p.fail(req.ID, codeInvalidParams, "invalid params: "+err.Error())
				return
			}
		}
		if params.ID == "" {
			p.fail(req.ID, codeInvalidParams, "session id is required")
			return
		}
		branches, err := typed.Branches(params.ID)
		if err != nil {
			p.fail(req.ID, codeServerError, scrubSessionLoadError(params.ID, err))
			return
		}
		if branches == nil {
			branches = []SessionBranch{}
		}
		p.respond(req.ID, map[string]any{"branches": branches})
	case "approval.list":
		// approval.list {session?} 返回 runner 的持久待审批
		// 镜像，使重连的客户端可以恢复它离开期间被问到的问题。
		// 存储通过一个可选的小接口发现，
		// 模仿 EventHub()：没有该接口的假实现和独立调用方会得到
		// 干净的 "not configured" 错误，而不是 panic。
		var params struct {
			Session string `json:"session"`
		}
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &params); err != nil {
				p.fail(req.ID, codeInvalidParams, "invalid params: "+err.Error())
				return
			}
		}
		typed, ok := cfg.Runner.(interface{ PendingStore() *security.PendingStore })
		if !ok || typed.PendingStore() == nil {
			p.fail(req.ID, codeServerError, "pending approval store not configured")
			return
		}
		items, err := typed.PendingStore().List(approvalSession(params.Session, cfg.Runner))
		if err != nil {
			p.fail(req.ID, codeInternalError, err.Error())
			return
		}
		if items == nil {
			items = []security.PendingApproval{}
		}
		p.respond(req.ID, map[string]any{"pending": items})
	case "approval.answer":
		// approval.answer {session?, id, granted} 记录操作员对某个
		// 待处理工具调用的决定。存储通过与 approval.list 相同的
		// 可选接口发现，因此假实现/独立调用方会得到
		// 干净的 "not configured" 错误，而不是 panic。
		//
		// PendingStore.Resolve 只持久化决定（移除待处理项，
		// 并追加一条决定记录）；它不会唤醒阻塞在 Approve.Ask 上的
		// 内存审批者。在 --rpc 路径上，agent 自己的
		// approval:resolved 通知只在 Ask 返回后才发出（而且对 --rpc
		// 使用的受保护工具集，发出审批通知的受保护分支会被完全跳过），
		// 因此引擎不在这里发送。我们自行发布一条 resolved 通知，
		// 使纯文本的消费者（原生桥）
		// 与已回答的卡片保持同步。
		var params struct {
			Session string `json:"session"`
			ID      string `json:"id"`
			Granted *bool  `json:"granted"`
		}
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &params); err != nil {
				p.fail(req.ID, codeInvalidParams, "invalid params: "+err.Error())
				return
			}
		}
		if strings.TrimSpace(params.ID) == "" {
			p.fail(req.ID, codeInvalidParams, "approval id is required")
			return
		}
		if params.Granted == nil {
			p.fail(req.ID, codeInvalidParams, "granted is required")
			return
		}
		typed, ok := cfg.Runner.(interface{ PendingStore() *security.PendingStore })
		if !ok || typed.PendingStore() == nil {
			p.fail(req.ID, codeServerError, "pending approval store not configured")
			return
		}
		store := typed.PendingStore()
		granted := *params.Granted
		session := approvalSession(params.Session, cfg.Runner)
		// Resolve 会删除该项，因此先读取待批镜像：一方面取工具名供
		// resolved 通知与未完成审批请求配对，另一方面用它做存在性校验。
		// 这一步是 P0 修复：若只检查 id 非空就直接 Resolve，本地 RPC
		// 只要猜中可预测的 id（如 bash-1）就能翻转一个待批决定，
		// 并借 resolved 通知让原生端撤下审批卡片。
		items, err := store.List(session)
		if err != nil {
			// 拿不到待批列表时无法确认 id 归属，失败关闭而不是放行。
			// PendingStore.List 的返回值就是该会话的待处理审批集合，
			// 因此这里无需 Approver.Exists(id)：精确匹配已足够。
			p.fail(req.ID, codeInternalError, err.Error())
			return
		}
		found := false
		tool := ""
		for _, item := range items {
			if item.ID == params.ID {
				found = true
				tool = item.Tool
				break
			}
		}
		if !found {
			p.fail(req.ID, codeInvalidParams, fmt.Sprintf("approval id %q is not pending", params.ID))
			return
		}
		if err := store.Resolve(session, params.ID, granted); err != nil {
			p.fail(req.ID, codeInternalError, err.Error())
			return
		}
		hub.Publish(agent.ApprovalResolvedEvent(tool, params.ID, granted))
		p.respond(req.ID, map[string]any{"ok": true})
	case "event/subscribe":
		// 在 goroutine 中流式处理可使扫描器继续读取后续请求。
		// 方法结果报告当前 seq；之后每个事件作为
		// "event" 通知到达。取消传输（EOF 或 ctx）会使其停止。
		var params struct {
			After uint64 `json:"after"`
		}
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &params); err != nil {
				p.fail(req.ID, codeInvalidParams, "invalid params: "+err.Error())
				return
			}
		}
		id := req.ID
		wg.Add(1)
		go func() {
			defer wg.Done()
			// 兜底恢复：订阅流中的 panic 不得终结整个 --rpc 进程。
			// 下方 defer stop() 在本 defer 之后注册，故按 stop → recover →
			// wg.Done 的顺序执行：退订关闭通道、wg.Done 释放等待者都不被跳过，
			// 客户端在 panic 后仍能正常结束传输而不会挂起。
			defer func() {
				if r := recover(); r != nil {
					logRecoveredPanic("rpc event/subscribe", r)
				}
			}()
			ch, stop, seqAt := hub.SubscribeAfter(params.After)
			defer stop()
			// seqAt 与订阅一起捕获，因此在该结果写出之前
			// 的间隙中发布的事件不会在重连时被跳过。
			p.respond(id, map[string]any{"seq": seqAt})
			for {
				select {
				case <-ctx.Done():
					return
				case ev, ok := <-ch:
					if !ok {
						return
					}
					p.notify("event", map[string]any{"kind": ev.Kind, "event": ev})
				}
			}
		}()
	case "config.info":
		// config.info 无参数，返回当前会话真实使用的 model/provider/workspace，
		// 供原生窗口状态栏显示。来源通过可选接口注入：未注入时返回干净的
		// "not configured" 错误（与 session.tree 相同的零值语义），既有方法
		// 的 wire 形态逐字节不变。
		//
		// workspace 走与 session.load 相同的脱敏边界：绝对路径被折叠为
		// <storage>/... 占位符，凭据形态的片段先脱敏，避免用户主目录或
		// 存储位置跨越 RPC 边界。model/provider 同样过一遍，防御性处理。
		if cfg.ConfigInfoProvider == nil {
			p.fail(req.ID, codeServerError, "config info not configured")
			return
		}
		info := cfg.ConfigInfoProvider.ConfigInfo()
		info.Model = scrubAbsolutePaths(info.Model)
		info.Provider = scrubAbsolutePaths(info.Provider)
		info.Workspace = scrubAbsolutePaths(info.Workspace)
		p.respond(req.ID, info)
	case "status":
		// status 无参数，返回当前状态快照 JSON。来源通过可选接口注入：
		// 未注入时返回干净的 "not configured" 错误（与 config.info 的零值
		// 语义一致），既有方法的 wire 形态逐字节不变。
		//
		// 以 json.RawMessage 承载提供者字节，使其作为 result 的 JSON 对象
		// 直接嵌入而非变成字符串；CJK 保持 UTF-8 原样（json.Marshal 只转义
		// <、>、& 与 U+2028/U+2029，不转义汉字）。
		if cfg.StatusProvider == nil {
			p.fail(req.ID, codeServerError, "status not configured")
			return
		}
		p.respond(req.ID, json.RawMessage(cfg.StatusProvider.StatusJSON()))
	default:
		p.fail(req.ID, codeMethodNotFound, fmt.Sprintf("unknown method %q", req.Method))
	}
}
