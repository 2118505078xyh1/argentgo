// Package netproxy 提供一个只用标准库实现的本地 HTTP/CONNECT 代理，
// 让不受工具注册表约束的子进程（curl、node、python 等）的出网也走同一条
// 安全链。代理本体不发起任何请求决策：它把每次出网交给 security.Chain
// 做策略判定、真人审批与审计（TODO #6）。
package netproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// BlockedRequest 描述一次被拦截的出网，传给审计/取消钩子。
type BlockedRequest struct {
	Method   string
	URL      string
	Decision string
	Reason   string
}

// Hook 是拦截时调用的钩子。它不影响判定，只做记录/通知；必须快速返回。
type Hook func(BlockedRequest)

// Policy 是网络策略的最小契约，由 security.NetPolicy 实现；测试可注入桩。
type Policy interface {
	CheckURL(raw, method string) security.NetResult
}

// Options 配置代理。零值可用：默认策略拒绝私网与云元数据、放行公网。
type Options struct {
	Chain   *security.Chain
	Policy  Policy
	Limited bool // 为真时只允许 GET/HEAD/OPTIONS
	OnBlock Hook
	Session string
	// Cancel 关闭时立即停止代理并断开所有在途连接。
	Cancel <-chan struct{}
}

// Proxy 是运行中的本地代理。
type Proxy struct {
	opts      Options
	ln        net.Listener
	server    *http.Server
	transport *http.Transport

	mu     sync.Mutex
	closed bool
	conns  map[net.Conn]bool
	done   chan struct{}
}

// New 在 127.0.0.1 的随机端口上启动代理。调用方必须 Close 以释放监听与
// 在途连接；Cancel 渠道关闭时也会自动 Close。
func New(opts Options) (*Proxy, error) {
	if opts.Policy == nil {
		opts.Policy = security.DefaultNetPolicy()
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("netproxy: listen: %w", err)
	}
	p := &Proxy{
		opts:  opts,
		ln:    ln,
		done:  make(chan struct{}),
		conns: make(map[net.Conn]bool),
		transport: &http.Transport{
			Proxy:               nil, // 转发不走系统代理，避免环回自身
			DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout: 10 * time.Second,
			MaxIdleConns:        16,
			IdleConnTimeout:     30 * time.Second,
		},
	}
	p.server = &http.Server{Handler: p, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = p.server.Serve(ln) }()
	if opts.Cancel != nil {
		go func() {
			select {
			case <-opts.Cancel:
				_ = p.Close()
			case <-p.done:
			}
		}()
	}
	return p, nil
}

// Addr 返回代理监听地址（host:port）。
func (p *Proxy) Addr() string { return p.ln.Addr().String() }

// Env 返回指向本代理的环境变量集合。NO_PROXY 保留本地回环直连，
// 避免把代理自身的控制面或本机服务也绕进代理。
func (p *Proxy) Env() Env {
	addr := "http://" + p.Addr()
	return Env{HTTPProxy: addr, HTTPSProxy: addr, NoProxy: "localhost,127.0.0.1,::1"}
}

// Close 停止代理并关闭所有被劫持的连接。可安全重复调用。
func (p *Proxy) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	conns := make([]net.Conn, 0, len(p.conns))
	for conn := range p.conns {
		conns = append(conns, conn)
	}
	p.conns = make(map[net.Conn]bool)
	p.mu.Unlock()
	close(p.done)
	p.transport.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = p.server.Shutdown(ctx)
	for _, conn := range conns {
		_ = conn.Close()
	}
	return nil
}

func (p *Proxy) track(conn net.Conn) func() {
	p.mu.Lock()
	p.conns[conn] = true
	p.mu.Unlock()
	return func() {
		p.mu.Lock()
		delete(p.conns, conn)
		p.mu.Unlock()
		_ = conn.Close()
	}
}

// ServeHTTP 处理普通 HTTP 请求与 HTTPS 的 CONNECT 隧道。
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.handleConnect(w, r)
		return
	}
	p.handleHTTP(w, r)
}

// handleConnect 为 HTTPS 建立隧道。CONNECT 里看不到具体 HTTP 方法，
// 因此按目标主机做策略检查；Limited 模式对隧道同样生效（只允许已授权
// 的主机，方法限制在隧道内部由目标服务器自行处理）。
func (p *Proxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	target := r.Host
	if err := p.check(r.Context(), "https://"+target, http.MethodConnect); err != nil {
		writeForbidden(w, err)
		return
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	dest, err := dialer.DialContext(r.Context(), "tcp", target)
	if err != nil {
		http.Error(w, "netproxy: dial target: "+err.Error(), http.StatusBadGateway)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		_ = dest.Close()
		http.Error(w, "netproxy: hijack unsupported", http.StatusInternalServerError)
		return
	}
	client, bufrw, err := hijacker.Hijack()
	if err != nil {
		_ = dest.Close()
		return
	}
	releaseClient := p.track(client)
	releaseDest := p.track(dest)
	defer releaseClient()
	defer releaseDest()

	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	_ = bufrw.Flush()

	// 取消钩子：请求 context 结束或代理被关闭时，立刻断开两端，
	// 使两个 Copy goroutine 都退出，不留下泄漏。
	stop := make(chan struct{})
	go func() {
		select {
		case <-r.Context().Done():
			_ = client.Close()
			_ = dest.Close()
		case <-p.done:
			_ = client.Close()
			_ = dest.Close()
		case <-stop:
		}
	}()
	defer close(stop)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(dest, bufrw)
		if tcp, ok := dest.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(client, dest)
		if tcp, ok := client.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
	}()
	wg.Wait()
}

// handleHTTP 转发普通 HTTP 请求。它重建请求以去掉入站代理特有的形态，
// 并把响应头与状态码原样回传。
func (p *Proxy) handleHTTP(w http.ResponseWriter, r *http.Request) {
	targetURL := "http://" + r.Host + r.URL.RequestURI()
	if err := p.check(r.Context(), targetURL, r.Method); err != nil {
		writeForbidden(w, err)
		return
	}
	out, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, r.Body)
	if err != nil {
		http.Error(w, "netproxy: build request: "+err.Error(), http.StatusBadRequest)
		return
	}
	for key, values := range r.Header {
		for _, value := range values {
			out.Header.Add(key, value)
		}
	}
	out.Header.Del("Proxy-Connection")
	out.Header.Del("Proxy-Authorization")
	resp, err := p.transport.RoundTrip(out)
	if err != nil {
		http.Error(w, "netproxy: round trip: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// check 执行一次策略判定 → 审批 → 审计。任何非 Allow 判定都返回错误，
// 由调用方转成 403；绝不在判定失败后继续建立连接。
func (p *Proxy) check(ctx context.Context, rawURL, method string) error {
	result := p.opts.Policy.CheckURL(rawURL, method)
	if p.opts.Limited && !limitedMethod(method) {
		result = security.NetResult{
			Decision: security.Deny,
			Reason:   "network method " + method + " is not allowed in limited mode",
			Code:     security.NetReasonPolicyDeny,
		}
	}
	args, _ := json.Marshal(map[string]string{"url": rawURL, "method": method})
	reason := result.Reason
	if reason == "" {
		reason = "network egress denied by proxy policy"
	}
	switch result.Decision {
	case security.Deny:
		p.audit("deny", reason, args)
		p.block(method, rawURL, "deny", reason)
		return &security.StopError{Tool: "netproxy", Reason: reason}
	case security.Ask:
		if p.opts.Chain == nil {
			p.audit("deny", "approval required but no chain configured", args)
			p.block(method, rawURL, "deny", "approval required but no chain configured")
			return &security.StopError{Tool: "netproxy", Reason: "approval required but no chain configured"}
		}
		checkCtx := ctx
		if result.Rule != nil && result.Rule.RequireHuman {
			checkCtx = security.WithRequireHuman(checkCtx)
		}
		if err := p.opts.Chain.Check(checkCtx, "netproxy", args); err != nil {
			p.audit("deny", "approval failed: "+err.Error(), args)
			p.block(method, rawURL, "deny", "approval failed")
			return err
		}
		return nil
	default:
		p.audit("allow", "network egress allowed by proxy policy", args)
		return nil
	}
}

func (p *Proxy) audit(decision, reason string, args []byte) {
	if p.opts.Chain != nil {
		p.opts.Chain.RecordDecision("netproxy", decision, reason, args)
	}
}

func (p *Proxy) block(method, rawURL, decision, reason string) {
	if p.opts.OnBlock != nil {
		p.opts.OnBlock(BlockedRequest{Method: method, URL: rawURL, Decision: decision, Reason: reason})
	}
}

func limitedMethod(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

func writeForbidden(w http.ResponseWriter, err error) {
	http.Error(w, "netproxy: "+err.Error(), http.StatusForbidden)
}

// 编译期确认代理满足标准库 Handler 契约。
var _ http.Handler = (*Proxy)(nil)
