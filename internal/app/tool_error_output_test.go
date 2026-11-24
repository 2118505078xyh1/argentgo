package app

import "testing"

func TestToolErrorOutputEnabled(t *testing.T) {
	cases := []struct {
		name string
		env  func(string) string
		want bool
	}{
		{"unset", func(string) string { return "" }, true},
		{"one", func(string) string { return "1" }, true},
		{"zero", func(string) string { return "0" }, false},
		{"false", func(string) string { return "false" }, false},
		{"off with spaces", func(string) string { return " OFF " }, false},
		{"nil env", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := toolErrorOutputEnabled(tc.env); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
