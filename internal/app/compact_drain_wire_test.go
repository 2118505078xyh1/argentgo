package app

import "testing"

func TestCompactDrainEnabledFromEnv(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"", true}, {"1", true}, {"true", true}, {"YES", true},
		{"  on  ", true}, {"maybe", true}, {"0", false}, {"false", false}, {" NO ", false}, {"off", false},
	}
	for _, tc := range cases {
		env := func(string) string { return tc.raw }
		if got := compactDrainEnabledFromEnv(env); got != tc.want {
			t.Errorf("enabled(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
	if !compactDrainEnabledFromEnv(nil) {
		t.Fatal("nil env must default to on")
	}
}
