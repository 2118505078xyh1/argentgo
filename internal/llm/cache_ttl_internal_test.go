package llm

import "testing"

func TestNormalizeCacheTTL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty keeps the provider default", "", ""},
		{"whitespace keeps the provider default", "   ", ""},
		{"five minutes", "5m", "5m"},
		{"thirty minutes", "30m", "30m"},
		{"one hour", "1h", "1h"},
		{"one day", "24h", "24h"},
		{"surrounding whitespace is trimmed", " 1h ", "1h"},
		{"unsupported short tier is rejected", "10s", ""},
		{"unsupported long tier is rejected", "7d", ""},
		{"free form text is rejected", "soon", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeCacheTTL(tc.in); got != tc.want {
				t.Fatalf("normalizeCacheTTL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
