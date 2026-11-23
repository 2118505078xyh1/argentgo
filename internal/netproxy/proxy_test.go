package netproxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

type allowPolicy struct{}

func (allowPolicy) CheckURL(string, string) security.NetResult {
	return security.NetResult{Decision: security.Allow}
}

type denyPolicy struct{}

func (denyPolicy) CheckURL(string, string) security.NetResult {
	return security.NetResult{
		Decision: security.Deny,
		Reason:   "blocked by test policy",
		Code:     security.NetReasonPolicyDeny,
	}
}

func proxyClient(t *testing.T, p *Proxy) *http.Client {
	t.Helper()
	u, err := url.Parse(p.Env().HTTPProxy)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{Proxy: http.ProxyURL(u)},
	}
}

func TestProxyBlockedRequestTriggersHookAndAudit(t *testing.T) {
	dir := t.TempDir()
	chain := security.NewChain(security.DefaultPolicy(dir), nil, security.NewAuditLog(dir), "s1")
	var blocks []BlockedRequest
	p, err := New(Options{
		Chain:   chain,
		Policy:  denyPolicy{},
		OnBlock: func(b BlockedRequest) { blocks = append(blocks, b) },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	resp, err := proxyClient(t, p).Get("http://blocked.example/x")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("blocked host must return 403, got %d", resp.StatusCode)
	}
	if len(blocks) != 1 || blocks[0].URL != "http://blocked.example/x" {
		t.Fatalf("block hook missing or wrong: %+v", blocks)
	}
	entries, err := security.ReadAudit(chain.Audit.Path())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 || entries[0].Decision != "deny" {
		t.Fatalf("audit must record deny: %+v", entries)
	}
}

func TestProxyLimitedModeRejectsMutatingMethods(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	p, err := New(Options{Policy: allowPolicy{}, Limited: true})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	client := proxyClient(t, p)
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		req, _ := http.NewRequest(method, upstream.URL+"/", nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("limited mode must allow %s: %v", method, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusForbidden {
			t.Fatalf("limited mode must allow %s", method)
		}
	}
	req, _ := http.NewRequest(http.MethodPost, upstream.URL+"/", strings.NewReader("x"))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("limited mode must reject POST, got %d", resp.StatusCode)
	}
}

func TestProxyForwardsAllowedHTTP(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "hello:"+r.URL.Path)
	}))
	defer upstream.Close()
	p, err := New(Options{Policy: allowPolicy{}})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	resp, err := proxyClient(t, p).Get(upstream.URL + "/world")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello:/world" {
		t.Fatalf("unexpected body %q", body)
	}
}

func TestProxyConnectTunnelAndCancel(t *testing.T) {
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			conn, err := echo.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()

	cancel := make(chan struct{})
	p, err := New(Options{Policy: allowPolicy{}, Cancel: cancel})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	conn, err := net.DialTimeout("tcp", p.Addr(), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", echo.Addr(), echo.Addr())
	if _, err := io.WriteString(conn, req); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	status, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(status, "200") {
		t.Fatalf("CONNECT failed: status=%q err=%v", status, err)
	}
	// 吃掉 200 响应头后的空行。
	if blank, err := reader.ReadString('\n'); err != nil || strings.TrimSpace(blank) != "" {
		t.Fatalf("bad CONNECT blank line: %q err=%v", blank, err)
	}
	if _, err := io.WriteString(conn, "ping"); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(reader, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("tunnel echo failed: %q err=%v", buf, err)
	}

	// 取消钩子必须让代理关闭监听，且 Close 可重复调用不阻塞。
	close(cancel)
	done := make(chan struct{})
	go func() { _ = p.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not close the proxy")
	}
}

func TestEnvApplyOverridesInheritedProxy(t *testing.T) {
	env := Env{HTTPProxy: "http://127.0.0.1:1234", HTTPSProxy: "http://127.0.0.1:1234", NoProxy: "localhost"}
	base := []string{"HTTP_PROXY=http://old:8080", "PATH=/bin"}
	out := env.Apply(base)
	joined := strings.Join(out, "\n")
	if !strings.Contains(joined, "HTTP_PROXY=http://127.0.0.1:1234") ||
		!strings.Contains(joined, "http_proxy=http://127.0.0.1:1234") ||
		!strings.Contains(joined, "NO_PROXY=localhost") {
		t.Fatalf("proxy env not injected: %q", out)
	}
	if len(base) != 2 {
		t.Fatal("Apply must not mutate its input")
	}
}

func TestDisabledToggle(t *testing.T) {
	t.Setenv("ARGENT_NETPROXY", "0")
	if !disabled() {
		t.Fatal("ARGENT_NETPROXY=0 must disable the proxy")
	}
	if got := DefaultEnv(context.Background()); !got.IsZero() {
		t.Fatalf("disabled default env must be zero, got %+v", got)
	}
}
