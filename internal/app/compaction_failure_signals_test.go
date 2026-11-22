package app

import "testing"

func TestFailureSignalsEnabled(t *testing.T) {
	cases := []struct {
		env  string
		want bool
	}{
		{"", false},
		{"0", false},
		{"1", true},
		{" 1 ", true},
	}
	for _, tc := range cases {
		got := failureSignalsEnabled(func(string) string { return tc.env })
		if got != tc.want {
			t.Errorf("env %q -> %v, want %v", tc.env, got, tc.want)
		}
	}
	if failureSignalsEnabled(nil) {
		t.Error("nil env should be false")
	}
}
