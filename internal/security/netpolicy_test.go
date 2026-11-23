package security

import (
	"context"
	"errors"
	"testing"
)

func TestNetPolicyBuiltinDeny(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want NetReason
	}{
		{"loopback", "http://127.0.0.1:8080/x", NetReasonPrivateAddress},
		{"private rfc1918", "https://10.0.0.5/a", NetReasonPrivateAddress},
		{"ipv6 loopback", "http://[::1]/", NetReasonPrivateAddress},
		{"cloud metadata v4", "http://169.254.169.254/latest/meta-data/", NetReasonCloudMetadata},
		{"cloud metadata dns", "http://metadata.google.internal/computeMetadata", NetReasonCloudMetadata},
		{"malformed", "://not a url", NetReasonMalformedURL},
		{"unsupported scheme", "ftp://example.com/a", NetReasonUnsupportedScheme},
		{"missing host", "http:///path", NetReasonMissingHost},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := CheckURL(tt.url, "GET")
			if res.Decision != Deny || res.Code != tt.want {
				t.Fatalf("CheckURL(%q) = %+v, want deny/%s", tt.url, res, tt.want)
			}
			if res.Reason == "" {
				t.Fatal("deny must carry a structured reason")
			}
		})
	}
}

func TestNetPolicyStrictestWins(t *testing.T) {
	policy := NewNetPolicy(NetPolicyConfig{Rules: []NetRule{
		{Decision: Allow, HostGlob: "*.example.com"},
		{Decision: Ask, HostGlob: "api.example.com"},
		{Decision: Allow, HostGlob: "api.example.com", Method: "GET"},
	}})
	// allow 行不会缓和后面的 ask；strictest-wins 要求 ask 胜出。
	res := policy.CheckURL("https://api.example.com/v1", "GET")
	if res.Decision != Ask {
		t.Fatalf("strictest-wins: got %v, want ask (%s)", res.Decision, res.Reason)
	}
	// 不匹配的主机落到默认放行。
	if got := policy.CheckURL("https://other.org/", "GET").Decision; got != Allow {
		t.Fatalf("unmatched host: got %v, want allow", got)
	}
}

func TestNetPolicyRuleFields(t *testing.T) {
	policy := NewNetPolicy(NetPolicyConfig{Rules: []NetRule{
		{Decision: Deny, HostGlob: "blocked.example", Protocol: "https", Port: 8443},
		{Decision: Deny, HostGlob: "mutating.example", Method: "POST"},
	}})
	tests := []struct {
		name   string
		url    string
		method string
		want   Decision
	}{
		{"deny host port protocol", "https://blocked.example:8443/x", "GET", Deny},
		{"wrong port not matched", "https://blocked.example:443/x", "GET", Allow},
		{"wrong protocol not matched", "http://blocked.example:8443/x", "GET", Allow},
		{"deny by method", "https://mutating.example/x", "POST", Deny},
		{"other method allowed", "https://mutating.example/x", "GET", Allow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := policy.CheckURL(tt.url, tt.method).Decision; got != tt.want {
				t.Fatalf("CheckURL(%q,%q) = %v, want %v", tt.url, tt.method, got, tt.want)
			}
		})
	}
}

func TestCheckNetworkEgressAllowLocalCannotOverridePolicy(t *testing.T) {
	policy := NewNetPolicy(NetPolicyConfig{Rules: []NetRule{
		{Decision: Deny, HostGlob: "blocked.example"},
		{Decision: Deny, HostGlob: "169.254.169.254"},
	}})
	ctx := WithNetworkGuard(context.Background(), NewNetworkGuard(policy, nil))
	if err := CheckNetworkEgress(ctx, "https://blocked.example", "GET", true); err == nil {
		t.Fatal("explicit deny must survive allowLocal=true")
	}
	if err := CheckNetworkEgress(ctx, "http://169.254.169.254/x", "GET", true); err == nil {
		t.Fatal("cloud metadata must survive allowLocal=true")
	}
	// 字面私网地址在 allowLocal 下放行。
	if err := CheckNetworkEgress(ctx, "http://127.0.0.1:8080", "GET", true); err != nil {
		t.Fatalf("allowLocal must permit literal loopback: %v", err)
	}
	if err := CheckNetworkEgress(ctx, "http://127.0.0.1:8080", "GET", false); err == nil {
		t.Fatal("private address must be denied by default")
	}
}

type egressApprover struct{ asked int }

func (a *egressApprover) Ask(context.Context, string, string) (bool, error) {
	a.asked++
	return true, nil
}

func TestCheckNetworkEgressAskGoesThroughChain(t *testing.T) {
	policy := NewNetPolicy(NetPolicyConfig{Rules: []NetRule{
		{Decision: Ask, HostGlob: "ask.example"},
		{Decision: Allow, HostGlob: "free.example"},
		{Decision: Deny, HostGlob: "blocked.example"},
	}})
	dir := t.TempDir()
	audit := NewAuditLog(dir)
	approver := &egressApprover{}
	chain := NewChain(DefaultPolicy(dir), approver, audit, "s1")
	ctx := WithNetworkGuard(context.Background(), NewNetworkGuard(policy, chain))

	if err := CheckNetworkEgress(ctx, "https://free.example", "GET", false); err != nil {
		t.Fatalf("allow must bypass approval: %v", err)
	}
	if approver.asked != 0 {
		t.Fatalf("allow rule must not ask, asked=%d", approver.asked)
	}
	if err := CheckNetworkEgress(ctx, "https://ask.example", "GET", false); err != nil {
		t.Fatalf("ask approval failed: %v", err)
	}
	if approver.asked != 1 {
		t.Fatalf("ask rule must ask once, asked=%d", approver.asked)
	}
	entries, err := ReadAudit(audit.Path())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 || entries[len(entries)-1].Decision != "allow" {
		t.Fatalf("audit missing final allow: %+v", entries)
	}
	var stop *StopError
	err = CheckNetworkEgress(ctx, "https://blocked.example", "GET", false)
	if !errors.As(err, &stop) {
		t.Fatalf("deny must be StopError, got %v", err)
	}
}
