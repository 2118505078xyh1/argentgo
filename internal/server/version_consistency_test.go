package server

import "testing"

func TestVersionsConsistent(t *testing.T) {
	tests := []struct {
		name          string
		tag           string
		binaryVersion string
		want          bool
	}{
		{"identical", "0.8.0", "0.8.0", true},
		{"leading v on tag", "v0.8.0", "0.8.0", true},
		{"leading V on tag", "V0.8.0", "0.8.0", true},
		{"leading v on binary", "0.8.0", "v0.8.0", true},
		{"leading V on both", "V0.8.0", "v0.8.0", true},
		{"surrounding whitespace", "  v0.8.0  ", "0.8.0", true},
		{"whitespace only difference", "v0.8.0 ", "\t0.8.0\n", true},
		{"case insensitive prerelease", "v0.8.0-RC1", "0.8.0-rc1", true},
		{"semver prerelease", "0.8.0-rc1", "0.8.0-rc1", true},
		{"semver build metadata", "v0.8.0+build.5", "0.8.0+build.5", true},
		{"mismatch patch", "v0.8.0", "0.8.1", false},
		{"mismatch minor", "v0.8.0", "0.9.0", false},
		{"prerelease vs release", "0.8.0-rc1", "0.8.0", false},
		{"empty tag", "", "0.8.0", false},
		{"empty binary version", "v0.8.0", "", false},
		{"both empty", "", "", false},
		{"whitespace tag", "  ", "0.8.0", false},
		{"whitespace binary", "v0.8.0", "\t ", false},
		{"only v", "v", "v", false},
		{"cjk tag", "版本", "0.8.0", false},
		{"cjk binary", "v0.8.0", "版本", false},
		{"cjk equal on both sides", "版本", "版本", false},
		{"cjk with v prefix", "v版本", "版本", false},
		{"full width digits", "０.８.０", "0.8.0", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := versionsConsistent(tt.tag, tt.binaryVersion); got != tt.want {
				t.Fatalf("versionsConsistent(%q, %q) = %v, want %v", tt.tag, tt.binaryVersion, got, tt.want)
			}
		})
	}
}

func TestNormalizeVersion(t *testing.T) {
	tests := []struct {
		input string
		want  string
		ok    bool
	}{
		{"  V0.8.0-RC1  ", "0.8.0-rc1", true},
		{"v", "", false},
		{"  ", "", false},
		{"版本", "", false},
		{"v版本", "", false},
	}
	for _, tt := range tests {
		got, ok := normalizeVersion(tt.input)
		if got != tt.want || ok != tt.ok {
			t.Fatalf("normalizeVersion(%q) = (%q, %v), want (%q, %v)", tt.input, got, ok, tt.want, tt.ok)
		}
	}
}
