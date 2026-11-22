package app

import (
	"net"
	"net/http"
	"net/http/pprof"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
)

// 运行时诊断 env 开关：诊断属于旁路能力，默认全部关闭，
// 任何非法配置都静默忽略，绝不影响主流程。
const (
	envPPROFAddr = "ARGENT_PPROF_ADDR"
	envGOGC      = "ARGENT_GOGC"
	envMemLimit  = "ARGENT_MEM_LIMIT"
)

// pprofServer 保存独立 pprof HTTP 服务的监听器与 server，供清理时统一关闭。
type pprofServer struct {
	ln  net.Listener
	srv *http.Server
}

// addr 返回实际监听地址；端口配置为 0 时可据此获知内核分配的临时端口。
func (s *pprofServer) addr() net.Addr { return s.ln.Addr() }

// stop 关闭 server 及其监听器与活跃连接；重复调用安全。
func (s *pprofServer) stop() { _ = s.srv.Close() }

// ApplyRuntimeDiagnostics 依据环境变量开启可选的运行时诊断能力（pprof、GC 百分比、内存上限）。
//
// 三个开关都由 env 注入，默认关闭：env 为空时不监听、不注册 handler、不改动运行时参数，
// 返回的清理函数为 no-op。env 非法值一律静默忽略，诊断失败不应让主流程崩溃。
// 返回的清理函数可重复调用，内部按开启的逆序释放资源（与 defer 顺序一致）。
func ApplyRuntimeDiagnostics(env func(string) string) func() {
	if env == nil {
		return func() {}
	}

	var (
		mu       sync.Mutex
		cleanups []func()
		closed   bool
	)
	// add 收集单项诊断的清理动作；清理动作可能来自不同开关，故加锁保护。
	add := func(fn func()) {
		mu.Lock()
		defer mu.Unlock()
		cleanups = append(cleanups, fn)
	}

	// pprof：只有显式给出 loopback 地址才启动，避免把诊断端点暴露到网络。
	if addr := strings.TrimSpace(env(envPPROFAddr)); addr != "" {
		if ps, ok := startPPROF(addr); ok {
			add(ps.stop)
		}
	}

	// GOGC：仅接受可解析的整数；debug.SetGCPercent 允许负值（关闭 GC），这里不做范围限制。
	if raw := strings.TrimSpace(env(envGOGC)); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			old := debug.SetGCPercent(n)
			add(func() { debug.SetGCPercent(old) })
		}
	}

	// 内存上限：单位为字节，必须是可解析的正整数；非正数对 SetMemoryLimit 无意义。
	if raw := strings.TrimSpace(env(envMemLimit)); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n > 0 {
			old := debug.SetMemoryLimit(n)
			add(func() { debug.SetMemoryLimit(old) })
		}
	}

	return func() {
		mu.Lock()
		if closed {
			mu.Unlock()
			return
		}
		closed = true
		fns := cleanups
		cleanups = nil
		mu.Unlock()
		// 逆序释放：先恢复运行时参数，再关闭 pprof 监听，避免清理过程中参数反复跳变。
		for i := len(fns) - 1; i >= 0; i-- {
			fns[i]()
		}
	}
}

// startPPROF 在独立的 http.ServeMux 上注册 pprof handler 并启动 HTTP 服务。
// 独立 mux 避免把 pprof 端点混进业务服务；仅接受 loopback 地址是安全底线。
// 第二个返回值表示是否真的启动了服务：地址非法或端口被占时返回 false，调用方据此零副作用跳过。
func startPPROF(addr string) (*pprofServer, bool) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || !isLoopbackHost(host) {
		return nil, false
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, false
	}

	// 与 net/http/pprof 的包初始化注册保持一致，但注册到我们自己的 mux 上。
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	ps := &pprofServer{
		ln:  ln,
		srv: &http.Server{Handler: mux},
	}
	go func() {
		// 监听器被关闭后 Serve 返回 ErrServerClosed，属于正常清理路径，忽略即可。
		_ = ps.srv.Serve(ln)
	}()
	return ps, true
}

// isLoopbackHost 只接受明确的 loopback 主机；空主机（":port"）会监听所有网卡，必须拒绝。
func isLoopbackHost(host string) bool {
	switch strings.ToLower(strings.TrimSpace(host)) {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}
