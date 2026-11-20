// Package server 承载 agent 的无界面传输层：--serve 背后的 HTTP/SSE
// 服务、--rpc 背后的换行分隔 JSON-RPC，以及 acp 背后的
// ACP 编辑器协议。
//
// 每条传输都驱动一个注入的 Runner，使本包独立于
// agent 循环，并可用假实现测试。
package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// DefaultAddr 是 Config.Addr 为空时使用的回环地址。
const DefaultAddr = "127.0.0.1:7777"

// maxBody 限制 JSON 端点请求体的大小。
const maxBody = 1 << 20

// Runner 是各传输所驱动的 agent 循环的切片。
type Runner interface {
	// Prompt 运行一轮，把流式文本转发给 onText，并返回
	// 最终答案。
	Prompt(ctx context.Context, text string, onText func(string)) (string, error)
	// Abort 取消当前正在运行的轮次（如果有）。
	Abort()
}

// Config 配置本包中的所有传输。
type Config struct {
	// Addr 是 --serve 的监听地址；空值表示 DefaultAddr。
	Addr string
	// Token 是 --serve 所需的 bearer token。它是必需的：Listen
	// 没有它会拒绝启动。
	Token string
	// Version 由 /v1/health、/v1/version 和 ACP initialize 报告。
	Version string
	// Runner 执行提示。必需。
	Runner Runner
	// Sessions 支撑 session.list / session.load RPC 方法。可选。
	Sessions SessionStore
	// Events 是 /v1/event 和 event/subscribe 共享的事件 hub。当为
	// nil 时，使用暴露 EventHub() *EventHub 的 runner（app.Factory 的 runner 就是这样），
	// 使所有无界面传输无需额外接线即可共享一个 hub。
	// 只有当两者都不存在时，每条传输才创建私有 hub。
	Events *EventHub
	// StructuredAbort 为 true 时，/v1/abort 返回由 abortResult 生成的
	// 结构化取消结果（含脱敏后的 reason，可经 ?reason= 传入）。默认
	// false 保持兼容的 {"ok":true} 响应，因此现有客户端与测试不受影响。
	StructuredAbort bool
}

// isCancelled 报告 Runner.Prompt 的错误是否意味着用户（或
// 传输层）停止了本轮，而非真正的失败。agent 循环
// 在中止时返回 context.Canceled（可能被包装），而客户端
// 断开后 ctx.Err() == context.Canceled。截止时间则是真正的失败，
// 在三种传输中都保留其原有的错误路径。
func isCancelled(ctx context.Context, err error) bool {
	return errors.Is(err, context.Canceled) || ctx.Err() == context.Canceled
}

// selectHub 解析某条传输发布到的 hub。显式的 Config.Events
// 优先；否则由 internal/app 构造的 runner 通过一个小接口
// 暴露其共享 hub，使本包不依赖接线方。该接口断言意味着
// 假实现和独立调用方保持原有的
// "私有 hub"行为。
func selectHub(cfg Config) *EventHub {
	if cfg.Events != nil {
		return cfg.Events
	}
	if typed, ok := cfg.Runner.(interface{ EventHub() *EventHub }); ok {
		if hub := typed.EventHub(); hub != nil {
			return hub
		}
	}
	return NewEventHub()
}

// Listen 绑定 HTTP 传输并返回服务器及其监听器。
// 未配置 bearer token 或 runner 时会失败。
func Listen(cfg Config) (*http.Server, net.Listener, error) {
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, nil, errors.New("server: a bearer token is required (--serve-token or ARGENT_SERVE_TOKEN)")
	}
	if cfg.Runner == nil {
		return nil, nil, errors.New("server: no runner configured")
	}
	addr := strings.TrimSpace(cfg.Addr)
	if addr == "" {
		addr = DefaultAddr
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("server: listen %s: %w", addr, err)
	}
	h := newHandler(cfg)
	srv := newServer(h)
	return srv, ln, nil
}

// ServeListener 一直服务直到 ctx 被取消或服务器失败。
func ServeListener(ctx context.Context, srv *http.Server, ln net.Listener) error {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdown)
		case <-done:
		}
	}()
	err := srv.Serve(ln)
	close(done)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Serve 在一次调用中完成监听与服务。
func Serve(ctx context.Context, cfg Config) error {
	srv, ln, err := Listen(cfg)
	if err != nil {
		return err
	}
	return ServeListener(ctx, srv, ln)
}

// NewHandler 构建 HTTP/SSE 处理器而不绑定 socket。
func NewHandler(cfg Config) http.Handler {
	return newHandler(cfg).routes()
}

// newHandler 创建各传输端点共享的、带状态的路由接收者。
// Listen 保留返回的指针，以便注册关闭
// 钩子；NewHandler 的独立调用方从不翻转该标志。
func newHandler(cfg Config) *handler {
	return &handler{cfg: cfg}
}

// newServer 为处理器构建 HTTP 服务器，并把关闭标志接到
// 标准的 Shutdown/Close 钩子。与 Listen 分开使
// 接线可以在不绑定 socket 的情况下被测试。
func newServer(h *handler) *http.Server {
	srv := &http.Server{
		Handler:           h.routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	// Shutdown（和 Close）会调用此钩子，因此在优雅关闭期间仍到达
	// 在途 keep-alive 连接的健康探测会观察到
	// "closing"，而不是误导性的 200。
	srv.RegisterOnShutdown(h.beginShutdown)
	return srv
}

func (h *handler) routes() http.Handler {
	hub := selectHub(h.cfg)
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/health", h.health)
	mux.HandleFunc("/v1/version", h.version)
	mux.HandleFunc("/v1/prompt", h.auth(h.prompt))
	mux.HandleFunc("/v1/abort", h.auth(h.abort))
	mux.HandleFunc("/v1/event", h.auth(NewEventHandler(hub).ServeHTTP))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})
	return mux
}

type handler struct {
	cfg     Config
	closing atomic.Bool
}

// beginShutdown 在 newServer 中被接到 http.Server.RegisterOnShutdown。
// 测试也可以直接调用它来驱动不依赖 socket 的处理器。
func (h *handler) beginShutdown() {
	h.closing.Store(true)
}

func (h *handler) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.authorized(r) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="argent"`)
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next(w, r)
	}
}

func (h *handler) authorized(r *http.Request) bool {
	want := strings.TrimSpace(h.cfg.Token)
	if want == "" {
		return false
	}
	// RFC 7235 把 auth-scheme 定义为不区分大小写，因此 "bearer"、"BEARER"
	// 和 "BeArEr" 都与 "Bearer" 是同一种方案。比较前缀时
	// 不对 token 本身做大小写折叠：token 保持逐字节精确。
	const scheme = "Bearer "
	header := r.Header.Get("Authorization")
	var got string
	if len(header) >= len(scheme) && strings.EqualFold(header[:len(scheme)], scheme) {
		got = strings.TrimSpace(header[len(scheme):])
	} else {
		// 带有不同方案的头部不是 bearer 凭据。
		// 把整个值保留为候选（如之前 TrimPrefix 所做的），使
		// 伪造的方案不会回退到 query string 中的 token。
		got = strings.TrimSpace(header)
	}
	if got == "" {
		got = strings.TrimSpace(r.URL.Query().Get("token"))
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func (h *handler) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	if h.closing.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status": "closing",
			"error":  "server is shutting down",
		})
		return
	}
	version := h.cfg.Version
	if version == "" {
		version = defaultVersion
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": version})
}

// version 提供结构化的构建报告。与 health 一样，它是诊断
// 端点，不需要 bearer token；它只暴露构建元数据，绝不
// 暴露凭据或配置。
func (h *handler) version(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	writeJSON(w, http.StatusOK, newVersionInfo(h.cfg.Version))
}

func (h *handler) abort(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if h.cfg.Runner == nil {
		writeError(w, http.StatusServiceUnavailable, "no runner configured")
		return
	}
	h.cfg.Runner.Abort()
	if h.cfg.StructuredAbort {
		// 可选接线：调用方显式开启后才返回结构化结果。reason 来自可选
		// 查询参数，由 abortResult 统一脱敏；未传时为空字符串。
		writeJSON(w, http.StatusOK, abortResult(true, r.URL.Query().Get("reason")))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// prompt 处理 POST /v1/prompt，流式返回 Server-Sent Events：
//
//	data: {"type":"text","text":"..."}
//	data: {"type":"done"}
//
// HTTP 200 头部会一直推迟到第一个文本事件（或终态的
// cancelled 事件）。如果 runner 在产生任何文本之前失败，客户端
// 会得到非 2xx 的 JSON 错误，而不是带错误事件的 200，因此
// 只看状态码的客户端不会把彻底失败误认为成功。一旦
// 流已开始，状态码就已提交，后续失败改用
// 终态 SSE 错误事件报告。用户中止仍是一次普通的 200 停止，
// 带 {"type":"cancelled"}。
func (h *handler) prompt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if h.cfg.Runner == nil {
		writeError(w, http.StatusServiceUnavailable, "no runner configured")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	text := requestText(body)
	if strings.TrimSpace(text) == "" {
		writeError(w, http.StatusBadRequest, "prompt has no text")
		return
	}

	flusher, _ := w.(http.Flusher)
	header := w.Header()

	// started 跟踪 SSE 响应头是否已上线。
	// 把它推迟到第一个事件，使流前的失败仍能
	// 选择诚实的非 2xx 状态和 JSON 体。
	started := false
	ensureStarted := func() {
		if started {
			return
		}
		header.Set("Content-Type", "text/event-stream")
		header.Set("Cache-Control", "no-cache")
		header.Set("Connection", "keep-alive")
		header.Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		started = true
	}

	emit := func(payload any) bool {
		line, err := json.Marshal(payload)
		if err != nil {
			return false
		}
		ensureStarted()
		if _, err := fmt.Fprintf(w, "data: %s\n\n", line); err != nil {
			return false
		}
		if flusher != nil {
			flusher.Flush()
		}
		return true
	}

	failed := false
	_, runErr := h.cfg.Runner.Prompt(r.Context(), text, func(chunk string) {
		if failed || chunk == "" {
			return
		}
		if !emit(map[string]any{"type": "text", "text": chunk}) {
			failed = true
		}
	})
	if failed {
		return
	}
	if runErr != nil {
		if isCancelled(r.Context(), runErr) {
			// Abort 不是失败；发出终态 cancelled 事件
			// 也会在中止前没有文本时提交 200 头部。
			emit(map[string]any{"type": "cancelled"})
			return
		}
		if started {
			// 流已经提交为 200，因此状态码无法再
			// 更改；用终态 SSE 错误事件结束。
			emit(map[string]any{"type": "error", "error": wireErrorText(runErr)})
			return
		}
		writeError(w, promptFailureStatus(runErr), wireErrorText(runErr))
		return
	}
	emit(map[string]any{"type": "done"})
}

// wireErrorText 在错误跨越传输边界之前对其进行脱敏。
//
// Runner 的失败直接来自提供方层，其错误体可能
// 回显服务器的 Authorization 头、API key、密码或账户邮箱。
// promptFailureStatus 已通过 llm.ClassifyAPIError 分类（它内部会脱敏），
// 但线协议体过去会原样携带 err.Error()——
// 因此只持有 bearer token 的客户端可以从失败的提示中读出服务器自己的
// 凭据。用分类器使用的同一套 llm 策略脱敏，使状态码和
// 体对什么是可安全披露的保持一致。
func wireErrorText(err error) string {
	return llm.RedactError(err)
}

// promptFailureStatus 把真正的流前 runner 失败映射到 HTTP
// 状态码。Auth（401）和限流（429）失败是可采取行动的客户端错误；
// 其他都是 500，使客户端无法把早期失败读成成功。
// 家族分类器使这与 CLI 的错误呈现保持一致。
func promptFailureStatus(err error) int {
	actionable := llm.ClassifyAPIError(err)
	if actionable == nil {
		return http.StatusInternalServerError
	}
	switch actionable.Family {
	case llm.FamilyAuth:
		return http.StatusUnauthorized
	case llm.FamilyRateLimit:
		return http.StatusTooManyRequests
	default:
		return http.StatusInternalServerError
	}
}

// requestText 是提示提取的 HTTP 侧。解析和裁剪
// 规则只存在于 promptText 中，使 HTTP、--rpc 和 acp 不会走偏。
func requestText(body []byte) string {
	return promptText(body)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}

func methodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	writeError(w, http.StatusMethodNotAllowed, "method not allowed")
}
