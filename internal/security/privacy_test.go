package security

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestIsLoopbackURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
		err  bool
	}{
		{"localhost", "http://localhost:8080/v1", true, false},
		{"localhost uppercase", "HTTP://LOCALHOST/v1", true, false},
		{"ipv4 loopback", "http://127.0.0.1", true, false},
		{"ipv4 loopback range", "https://127.42.1.2/v1", true, false},
		{"ipv6 loopback", "http://[::1]:8080", true, false},
		{"remote domain", "https://example.com/v1", false, false},
		{"remote ip", "http://8.8.8.8", false, false},
		{"non-http scheme", "ftp://localhost/file", false, true},
		{"file scheme", "file:///etc/passwd", false, true},
		{"malformed bracket", "http://[::1", false, true},
		{"malformed escape", "https://example.com/%zz", false, true},
		{"empty", "   ", false, true},
		{"no host", "http:///path", false, true},
		{"too long", "http://localhost/" + strings.Repeat("a", maxPrivacyURLBytes), false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := IsLoopbackURL(tc.in)
			if (err != nil) != tc.err {
				t.Fatalf("IsLoopbackURL(%q) err = %v, want error %t", tc.in, err, tc.err)
			}
			if got != tc.want {
				t.Fatalf("IsLoopbackURL(%q) = %t, want %t", tc.in, got, tc.want)
			}
		})
	}
}

func TestAllowRemote(t *testing.T) {
	const (
		loopback = "http://127.0.0.1:8080/v1"
		remote   = "https://api.example.com/v1"
		bad      = "http://[::1"
	)
	cases := []struct {
		name     string
		mode     PrivacyMode
		url      string
		override bool
		want     bool
		reason   string
	}{
		{"any/loopback", PrivacyAny, loopback, false, true, reasonPrivacyAny},
		{"any/remote", PrivacyAny, remote, false, true, reasonPrivacyAny},
		{"any/malformed", PrivacyAny, bad, false, true, reasonPrivacyAny},
		{"local/loopback", PrivacyLocalOnly, loopback, false, true, reasonPrivacyLoopback},
		{"local/remote", PrivacyLocalOnly, remote, false, false, reasonPrivacyRemoteBlocked},
		{"local/remote+override", PrivacyLocalOnly, remote, true, true, reasonPrivacyOverride},
		{"local/malformed", PrivacyLocalOnly, bad, false, false, reasonPrivacyInvalidURL},
		{"local/malformed+override", PrivacyLocalOnly, bad, true, true, reasonPrivacyOverride},
		{"unknown/override cannot widen", PrivacyMode("bogus"), remote, true, false, reasonPrivacyUnknownMode},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := AllowRemote(tc.mode, tc.url, tc.override)
			if got != tc.want || reason != tc.reason {
				t.Fatalf("AllowRemote = (%t, %q), want (%t, %q)", got, reason, tc.want, tc.reason)
			}
			if reason == "" {
				t.Fatal("reason must never be empty")
			}
		})
	}
}

func TestShouldBlockWebTool(t *testing.T) {
	if !ShouldBlockWebTool(PrivacyLocalOnly) {
		t.Fatal("local-only must block web tools")
	}
	if ShouldBlockWebTool(PrivacyAny) {
		t.Fatal("any must not block web tools")
	}
	if ShouldBlockWebTool("") {
		t.Fatal("the default (empty) posture must not block web tools")
	}
}

func TestPrivacyAuditReason(t *testing.T) {
	cases := []struct {
		mode    PrivacyMode
		changed bool
		want    string
	}{
		{PrivacyLocalOnly, true, "privacy_mode=local-only changed=true"},
		{PrivacyLocalOnly, false, "privacy_mode=local-only changed=false"},
		{PrivacyAny, false, "privacy_mode=any changed=false"},
		{"", false, "privacy_mode=any changed=false"},
	}
	for _, tc := range cases {
		got := PrivacyAuditReason(tc.mode, tc.changed)
		if got != tc.want {
			t.Fatalf("PrivacyAuditReason(%q, %t) = %q, want %q", tc.mode, tc.changed, got, tc.want)
		}
		if got != PrivacyAuditReason(tc.mode, tc.changed) {
			t.Fatalf("PrivacyAuditReason is not deterministic: %q", got)
		}
	}
}

func TestAuditEntryPrivacyModeJSON(t *testing.T) {
	without, err := json.Marshal(AuditEntry{TS: "2026-01-02T03:04:05Z", Tool: "bash", Decision: "allow"})
	if err != nil {
		t.Fatalf("marshal without privacy mode: %v", err)
	}
	if strings.Contains(string(without), "privacy_mode") {
		t.Fatalf("empty privacy mode must be omitted: %s", without)
	}

	with, err := json.Marshal(AuditEntry{
		TS:          "2026-01-02T03:04:05Z",
		Tool:        "web_fetch",
		Decision:    "deny",
		PrivacyMode: string(PrivacyLocalOnly),
	})
	if err != nil {
		t.Fatalf("marshal with privacy mode: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(with, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded["privacy_mode"] != "local-only" {
		t.Fatalf("privacy_mode = %v, want local-only: %s", decoded["privacy_mode"], with)
	}

	var roundTrip AuditEntry
	if err := json.Unmarshal(with, &roundTrip); err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if roundTrip.PrivacyMode != string(PrivacyLocalOnly) {
		t.Fatalf("round trip privacy mode = %q", roundTrip.PrivacyMode)
	}
}

func TestAuditPrivacyModeDoesNotLeakSecrets(t *testing.T) {
	dir := t.TempDir()
	log := NewAuditLog(dir)
	const secret = "sk-abcdef123456"
	err := log.Record(AuditEntry{
		Tool:        "web_fetch",
		Decision:    "deny",
		Reason:      PrivacyAuditReason(PrivacyLocalOnly, true) + " because " + secret,
		Args:        `{"url":"https://example.com","api_key":"` + secret + `"}`,
		PrivacyMode: string(PrivacyLocalOnly),
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	entries, err := ReadAudit(log.Path())
	if err != nil || len(entries) != 1 {
		t.Fatalf("read: %v entries=%d", err, len(entries))
	}
	entry := entries[0]
	if entry.PrivacyMode != string(PrivacyLocalOnly) {
		t.Fatalf("privacy mode lost: %q", entry.PrivacyMode)
	}
	if !strings.Contains(entry.Reason, "privacy_mode=local-only changed=true") {
		t.Fatalf("audit reason lost: %q", entry.Reason)
	}
	if strings.Contains(entry.Reason, secret) || strings.Contains(entry.Args, secret) {
		t.Fatalf("secret leaked into decoded entry: %+v", entry)
	}
	raw, err := os.ReadFile(log.Path())
	if err != nil {
		t.Fatalf("read raw log: %v", err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatalf("secret leaked into audit file: %s", raw)
	}
}
