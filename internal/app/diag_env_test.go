package app

import (
	"fmt"
	"net"
	"net/http"
	"runtime/debug"
	"strconv"
	"testing"
	"time"
)

// applyDiagnostics 用给定的三个 env 值调用 ApplyRuntimeDiagnostics，返回清理函数。
func applyDiagnostics(ppAddr, gc, mem string) func() {
	return ApplyRuntimeDiagnostics(func(key string) string {
		switch key {
		case envPPROFAddr:
			return ppAddr
		case envGOGC:
			return gc
		case envMemLimit:
			return mem
		}
		return ""
	})
}

// currentGCPercent 读回当前 GOGC。SetGCPercent(-1) 会顺带关闭 GC，
// 所以读到旧值后必须立刻恢复，避免污染同包其他测试。
func currentGCPercent() int {
	old := debug.SetGCPercent(-1)
	debug.SetGCPercent(old)
	return old
}

// currentMemLimit 读回当前内存上限。负数入参只读取、不改动限制。
func currentMemLimit() int64 { return debug.SetMemoryLimit(-1) }

// probeFreeAddr 借用一个临时端口探测其可用，随后立即释放，得到一个大概率空闲的 loopback 地址。
func probeFreeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("探测空闲端口失败: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("释放探测端口失败: %v", err)
	}
	return addr
}

// assertPortFree 断言清理后端口可再次绑定（确认监听已关闭）。
func assertPortFree(t *testing.T, addr string) {
	t.Helper()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("清理后端口 %s 仍被占用: %v", addr, err)
	}
	_ = ln.Close()
}

func httpGet(url string) (int, error) {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

// getStatusEventually 短暂重试，消除 HTTP server 启动 goroutine 的调度抖动。
func getStatusEventually(t *testing.T, url string, want int) {
	t.Helper()
	var last error
	for i := 0; i < 30; i++ {
		code, err := httpGet(url)
		if err == nil && code == want {
			return
		}
		last = err
		if err == nil {
			last = fmt.Errorf("状态码 %d，期望 %d", code, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("GET %s 未达到预期: %v", url, last)
}

// applyPPROFViaEnv 通过 env 启动 pprof，返回实际地址与清理函数。
// 端口先探测再占用存在极小竞态窗口，失败时换新端口重试，保证测试稳定。
func applyPPROFViaEnv(t *testing.T) (string, func()) {
	t.Helper()
	for attempt := 0; attempt < 5; attempt++ {
		addr := probeFreeAddr(t)
		cleanup := applyDiagnostics(addr, "", "")
		code, err := httpGet("http://" + addr + "/debug/pprof/heap")
		if err == nil && code == http.StatusOK {
			return addr, cleanup
		}
		cleanup()
	}
	t.Fatal("无法通过 env 启动 pprof 服务（重试 5 次）")
	return "", nil
}

func TestDiagEnvEmptyIsNoop(t *testing.T) {
	beforeGC, beforeMem := currentGCPercent(), currentMemLimit()
	probe := probeFreeAddr(t)

	cleanup := applyDiagnostics("", "", "")
	if cleanup == nil {
		t.Fatal("清理函数不能为 nil")
	}
	cleanup() // 重复调用必须安全（幂等）
	cleanup()

	if got := currentGCPercent(); got != beforeGC {
		t.Errorf("env 为空时不应改动 GOGC: got %d, want %d", got, beforeGC)
	}
	if got := currentMemLimit(); got != beforeMem {
		t.Errorf("env 为空时不应改动内存上限: got %d, want %d", got, beforeMem)
	}
	// 三个 env 全空时不应有任何监听抢占端口。
	assertPortFree(t, probe)
}

func TestDiagEnvNilEnvIsNoop(t *testing.T) {
	cleanup := ApplyRuntimeDiagnostics(nil)
	if cleanup == nil {
		t.Fatal("env 为 nil 时仍应返回非 nil 的清理函数")
	}
	cleanup()
}

func TestRuntimeDiagnosticsGOGCAndMemLimitRestore(t *testing.T) {
	beforeGC, beforeMem := currentGCPercent(), currentMemLimit()
	const wantGC = 4321
	const wantMem = int64(2 << 30) // 2 GiB，足够大，避免触发激进 GC 干扰测试

	cleanup := applyDiagnostics("", strconv.Itoa(wantGC), strconv.FormatInt(wantMem, 10))

	if got := currentGCPercent(); got != wantGC {
		t.Errorf("ARGENT_GOGC 未生效: got %d, want %d", got, wantGC)
	}
	if got := currentMemLimit(); got != wantMem {
		t.Errorf("ARGENT_MEM_LIMIT 未生效: got %d, want %d", got, wantMem)
	}

	cleanup()

	if got := currentGCPercent(); got != beforeGC {
		t.Errorf("清理后 GOGC 未恢复: got %d, want %d", got, beforeGC)
	}
	if got := currentMemLimit(); got != beforeMem {
		t.Errorf("清理后内存上限未恢复: got %d, want %d", got, beforeMem)
	}
}

func TestDiagEnvInvalidValuesIgnored(t *testing.T) {
	cases := []struct {
		name string
		gc   string
		mem  string
	}{
		{"garbage", "abc", "xyz"},
		{"float", "12.5", "1.5"},
		{"negative-mem", "100", "-1"},
		{"zero-mem", "100", "0"},
		{"overflow", "999999999999999999999999", "999999999999999999999999"},
		{"whitespace", "   ", "\t"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			beforeGC, beforeMem := currentGCPercent(), currentMemLimit()
			cleanup := applyDiagnostics("", tc.gc, tc.mem)

			if got := currentGCPercent(); got != beforeGC {
				t.Errorf("非法 GOGC=%q 不应改动设置: got %d, want %d", tc.gc, got, beforeGC)
			}
			if got := currentMemLimit(); got != beforeMem {
				t.Errorf("非法 MEM_LIMIT=%q 不应改动设置: got %d, want %d", tc.mem, got, beforeMem)
			}
			cleanup() // 非法值对应的清理也必须安全
		})
	}
}

func TestPPROFAddrLoopbackServesHeap(t *testing.T) {
	t.Run("via-env", func(t *testing.T) {
		addr, cleanup := applyPPROFViaEnv(t)
		defer cleanup()
		getStatusEventually(t, "http://"+addr+"/debug/pprof/heap", http.StatusOK)

		// 独立 mux 只注册 pprof 路由，业务路径应为 404。
		if code, err := httpGet("http://" + addr + "/not-a-pprof-path"); err != nil || code != http.StatusNotFound {
			t.Errorf("独立 mux 上非 pprof 路径应为 404: code=%d, err=%v", code, err)
		}

		cleanup()
		assertPortFree(t, addr)
	})

	t.Run("port-zero-discovers-addr", func(t *testing.T) {
		// 传入 :0 让内核分配端口，再从 startPPROF 返回的监听器读出实际地址。
		ps, ok := startPPROF("127.0.0.1:0")
		if !ok {
			t.Fatal("loopback :0 地址应成功启动 pprof")
		}
		addr := ps.addr().String()
		getStatusEventually(t, "http://"+addr+"/debug/pprof/heap", http.StatusOK)

		ps.stop()
		ps.stop() // 幂等
		assertPortFree(t, addr)
	})
}

func TestPPROFAddrLoopbackHostValidation(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"127.0.0.1", true},
		{"localhost", true},
		{"LOCALHOST", true},
		{"::1", true},
		{"0.0.0.0", false},
		{"192.168.1.10", false},
		{"127.0.0.2", false},
		{"", false},
		{"example.com", false},
	}
	for _, tc := range cases {
		t.Run(tc.host, func(t *testing.T) {
			if got := isLoopbackHost(tc.host); got != tc.want {
				t.Errorf("isLoopbackHost(%q) = %v, want %v", tc.host, got, tc.want)
			}
		})
	}
}

func TestPPROFAddrNonLoopbackRejected(t *testing.T) {
	addrs := []string{
		"0.0.0.0:0",        // 监听所有网卡
		":0",               // 空主机同样监听所有网卡
		"192.168.1.10:0",   // 局域网地址
		"127.0.0.2:0",      // 非白名单 loopback
		"example.com:8080", // 域名
		"127.0.0.1",        // 缺少端口
	}
	for _, addr := range addrs {
		t.Run(addr, func(t *testing.T) {
			if ps, ok := startPPROF(addr); ok {
				ps.stop()
				t.Fatalf("startPPROF(%q) 应拒绝非 loopback/非法地址", addr)
			}
		})
	}

	// 通过 env 传入非法地址也必须被静默忽略：无监听、清理安全。
	probe := probeFreeAddr(t)
	cleanup := applyDiagnostics("0.0.0.0:0", "", "")
	cleanup()
	assertPortFree(t, probe)
}
