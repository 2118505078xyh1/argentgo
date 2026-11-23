package app

import "testing"

func TestApprovalPersistEnabledFromEnv(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"", false}, {"1", true}, {"true", true}, {"  on  ", true},
		{"0", false}, {"false", false}, {"maybe", false},
	}
	for _, tc := range cases {
		env := func(string) string { return tc.raw }
		if got := approvalPersistEnabled(env); got != tc.want {
			t.Errorf("enabled(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
	if approvalPersistEnabled(nil) {
		t.Fatal("nil env must leave persistent approvals off")
	}
}
