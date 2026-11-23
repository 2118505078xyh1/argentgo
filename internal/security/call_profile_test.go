package security

import (
	"encoding/json"
	"testing"
)

func TestCallProfileIsZero(t *testing.T) {
	if !(CallProfile{}).IsZero() {
		t.Fatal("zero profile should be zero")
	}
	if (CallProfile{ReadOnly: true}).IsZero() {
		t.Fatal("ReadOnly profile not zero")
	}
}

func TestCheckWithProfile(t *testing.T) {
	p := NewPolicy(t.TempDir(), PolicyConfig{})
	args := json.RawMessage(`{"path":"a.txt"}`)
	// 默认与 Check 一致：read 工具 Allow。
	if p.Check("read", args) != Allow {
		t.Fatal("baseline read should allow")
	}
	if p.CheckWith("read", args, CallProfile{}) != Allow {
		t.Fatal("empty profile should allow")
	}
	// ReadOnly profile 拒绝修改类工具。
	if p.CheckWith("write", args, CallProfile{ReadOnly: true}) != Deny {
		t.Fatal("write should be denied under read-only profile")
	}
	// DenyPaths 收紧。
	prof := CallProfile{DenyPaths: []string{"**/.env"}}
	envArgs := json.RawMessage(`{"path":"x/.env"}`)
	if p.CheckWith("read", envArgs, prof) != Deny {
		t.Fatal("deny path should match")
	}
	if p.CheckWith("read", args, prof) != Allow {
		t.Fatal("non-matching path should allow")
	}
	// NoNetwork 拦截网络工具。
	if p.CheckWith("web_fetch", args, CallProfile{NoNetwork: true}) != Deny {
		t.Fatal("network tool should be denied")
	}
}
