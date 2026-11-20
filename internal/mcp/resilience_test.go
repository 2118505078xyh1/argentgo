package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestDefaultDiscoveryTimeouts(t *testing.T) {
	got := DefaultDiscoveryTimeouts()
	if got.Stdio != 30*time.Second || got.Remote != 5*time.Second {
		t.Fatalf("graded defaults = %+v, want stdio=30s remote=5s", got)
	}
	if got.Min != time.Second || got.Max != 2*time.Minute {
		t.Fatalf("clamp bounds = %+v, want [1s, 2m]", got)
	}
}

func TestEffectiveDiscoveryTimeoutGradedByTransport(t *testing.T) {
	cases := []struct {
		name string
		spec Spec
		want time.Duration
	}{
		{"stdio inferred from command", Spec{Command: "node"}, 30 * time.Second},
		{"stdio explicit", Spec{Transport: "stdio", Command: "node"}, 30 * time.Second},
		{"stdio case insensitive", Spec{Transport: "STDIO", Command: "node"}, 30 * time.Second},
		{"http from url", Spec{URL: "http://localhost:1"}, 5 * time.Second},
		{"http explicit", Spec{Transport: "http", URL: "http://localhost:1"}, 5 * time.Second},
		{"sse is remote", Spec{Transport: "sse", URL: "http://localhost:1"}, 5 * time.Second},
		{"empty spec defaults remote", Spec{}, 5 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.spec.EffectiveDiscoveryTimeout(); got != tc.want {
				t.Fatalf("EffectiveDiscoveryTimeout = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEffectiveDiscoveryTimeoutOverrideClamped(t *testing.T) {
	base := Spec{Transport: "stdio", Command: "node"}
	cases := []struct {
		name     string
		override time.Duration
		want     time.Duration
	}{
		{"no override uses graded default", 0, 30 * time.Second},
		{"negative uses graded default", -time.Second, 30 * time.Second},
		{"below minimum clamped up", 500 * time.Millisecond, time.Second},
		{"at minimum", time.Second, time.Second},
		{"inside range preserved", 10 * time.Second, 10 * time.Second},
		{"at maximum", 2 * time.Minute, 2 * time.Minute},
		{"above maximum clamped down", 5 * time.Minute, 2 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := base
			spec.DiscoveryTimeout = tc.override
			if got := spec.EffectiveDiscoveryTimeout(); got != tc.want {
				t.Fatalf("override %v: got %v, want %v", tc.override, got, tc.want)
			}
		})
	}
}

func TestDecodeConfigDiscoveryTimeout(t *testing.T) {
	raw := map[string]any{
		"mcpServers": map[string]any{
			"camel": map[string]any{"command": "a", "discoveryTimeout": 12},
			"snake": map[string]any{"url": "http://x", "discovery_timeout": "3"},
			"bad":   map[string]any{"command": "c", "discoveryTimeout": -1},
		},
	}
	specs, problems := DecodeConfig(raw)
	byName := map[string]Spec{}
	for _, spec := range specs {
		byName[spec.Name] = spec
	}
	if got := byName["camel"].DiscoveryTimeout; got != 12*time.Second {
		t.Fatalf("camel DiscoveryTimeout = %v, want 12s", got)
	}
	if got := byName["snake"].DiscoveryTimeout; got != 3*time.Second {
		t.Fatalf("snake DiscoveryTimeout = %v, want 3s", got)
	}
	if got := byName["bad"].DiscoveryTimeout; got != 0 {
		t.Fatalf("bad DiscoveryTimeout = %v, want 0 (ignored)", got)
	}
	var found bool
	for _, problem := range problems {
		if problem.Server == "bad" && strings.Contains(problem.Field, "discoveryTimeout") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing problem for bad discoveryTimeout: %+v", problems)
	}
}

// timeoutNetError 是一个 Timeout 方法返回 true 的 net.Error。
type timeoutNetError struct{}

func (timeoutNetError) Error() string   { return "i/o timeout" }
func (timeoutNetError) Timeout() bool   { return true }
func (timeoutNetError) Temporary() bool { return true }

func TestIsRetryableTransientVsPermanent(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"connection reset", syscall.ECONNRESET, true},
		{"timed out", syscall.ETIMEDOUT, true},
		{"context deadline", context.DeadlineExceeded, true},
		{"net timeout", timeoutNetError{}, true},
		{"wrapped reset", fmt.Errorf("send frame: %w", syscall.ECONNRESET), true},
		{"http 502 typed", &HTTPStatusError{StatusCode: 502, Message: "Bad Gateway"}, true},
		{"http 503 typed", &HTTPStatusError{StatusCode: 503, Message: "Service Unavailable"}, true},
		{"http 504 typed", &HTTPStatusError{StatusCode: 504, Message: "Gateway Timeout"}, true},
		{"http 503 wrapped typed", fmt.Errorf("mcp %q: initialize: %w", "x", &HTTPStatusError{StatusCode: 503}), true},
		{"http 503 current transport text", fmt.Errorf("mcp %q: initialize: %w", "x", errors.New("HTTP 503: unavailable")), true},
		{"http 401 permanent", &HTTPStatusError{StatusCode: 401, Message: "Unauthorized"}, false},
		{"http 403 permanent", &HTTPStatusError{StatusCode: 403, Message: "Forbidden"}, false},
		{"http 500 not in allowlist", &HTTPStatusError{StatusCode: 500, Message: "Internal Server Error"}, false},
		{"http 401 wrapped permanent", fmt.Errorf("call: %w", &HTTPStatusError{StatusCode: 401}), false},
		{"method not found permanent", &rpcErrorBody{Code: -32601, Message: "Method not found"}, false},
		{"method not found wrapped permanent", fmt.Errorf("call: %w", &rpcErrorBody{Code: -32601}), false},
		{"context canceled permanent", context.Canceled, false},
		{"generic error permanent", errors.New("boom"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsRetryable(tc.err); got != tc.want {
				t.Fatalf("IsRetryable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
