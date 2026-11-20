package cli

import (
	"io"
	"testing"
)

func TestServeAddressJoinsPortForBareIPv6(t *testing.T) {
	cases := []struct {
		name string
		host string
		port int
		want string
	}{
		{"default host", "", 7777, "127.0.0.1:7777"},
		{"ipv4", "127.0.0.1", 9090, "127.0.0.1:9090"},
		{"hostname", "example.com", 7777, "example.com:7777"},
		{"bare ipv6 loopback", "::1", 7777, "[::1]:7777"},
		{"bare ipv6 custom port", "::1", 9090, "[::1]:9090"},
		{"bare ipv6 global", "2001:db8::1", 8080, "[2001:db8::1]:8080"},
		{"bracketed ipv6", "[::1]", 9090, "[::1]:9090"},
		{"bracketed ipv6 with port", "[::1]:8080", 9090, "[::1]:8080"},
		{"ipv4 with port", "127.0.0.1:8080", 9090, "127.0.0.1:8080"},
		{"hostname with port", "example.com:8080", 7777, "example.com:8080"},
		{"whitespace around ipv6", "  ::1  ", 9090, "[::1]:9090"},
		{"zone scoped ipv6", "fe80::1%eth0", 7777, "[fe80::1%eth0]:7777"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := serveAddress(tc.host, tc.port); got != tc.want {
				t.Fatalf("serveAddress(%q, %d) = %q, want %q", tc.host, tc.port, got, tc.want)
			}
		})
	}
}

func TestParseArgsServeAddressWithIPv6(t *testing.T) {
	opts, _, err := parseArgs(
		[]string{"--serve", "--serve-address", "::1", "--serve-port", "9090"},
		func(string) string { return "" },
		io.Discard,
	)
	if err != nil {
		t.Fatal(err)
	}
	if opts.ServeAddr != "[::1]:9090" {
		t.Fatalf("ServeAddr = %q", opts.ServeAddr)
	}
}
