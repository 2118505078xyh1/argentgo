package update

import "testing"

func TestCompareVersionsTable(t *testing.T) {
	cases := []struct {
		left, right string
		want        int
	}{
		{"1.2.3", "1.2.3", 0},
		{"v1.2.3", "1.2.3", 0},
		{"V1.2.3", "v1.2.3", 0},
		{"1.2.3", "1.2.2", 1},
		{"1.2.2", "1.2.3", -1},
		{"1.10.0", "1.9.9", 1},
		{"2.0.0", "1.99.99", 1},
		{"1.2", "1.2.0", 0},
		{"1", "1.0.0", 0},
		{"1.2.3+build.5", "1.2.3", 0},
		{"1.2.3+alpha", "1.2.3+beta", 0},
		{"1.0.0-beta", "1.0.0", -1},
		{"1.0.0", "1.0.0-rc.1", 1},
		{"1.0.0-alpha", "1.0.0-beta", -1},
		{"1.0.0-beta.2", "1.0.0-beta.11", -1},
		{"1.0.0-beta", "1.0.0-beta.2", -1},
		{"1.0.0-alpha.1", "1.0.0-alpha.beta", -1},
		{"1.0.0-rc.1", "1.0.0-beta.11", 1},
		{"0.2.0", "0.1.9", 1},
	}
	for _, test := range cases {
		if got := CompareVersions(test.left, test.right); got != test.want {
			t.Fatalf("CompareVersions(%q, %q) = %d, want %d",
				test.left, test.right, got, test.want)
		}
		if got := CompareVersions(test.right, test.left); got != -test.want {
			t.Fatalf("CompareVersions(%q, %q) = %d, want %d",
				test.right, test.left, got, -test.want)
		}
	}
}

func TestCompareVersionsTreatsGarbageAsZero(t *testing.T) {
	if CompareVersions("not-a-version", "0.0.1") != -1 {
		t.Fatal("garbage version should sort below 0.0.1")
	}
	if CompareVersions("not-a-version", "also-not") != 0 {
		t.Fatal("two garbage versions should compare equal")
	}
}

func TestParseVersion(t *testing.T) {
	cases := []struct {
		input string
		want  Version
	}{
		{"1.2.3", Version{Major: 1, Minor: 2, Patch: 3}},
		{"v0.4.0", Version{Major: 0, Minor: 4, Patch: 0}},
		{"2", Version{Major: 2}},
		{"1.2.3-rc.1+build.7", Version{Major: 1, Minor: 2, Patch: 3, Pre: []string{"rc", "1"}}},
	}
	for _, test := range cases {
		got, err := ParseVersion(test.input)
		if err != nil {
			t.Fatalf("ParseVersion(%q): %v", test.input, err)
		}
		if got.Major != test.want.Major || got.Minor != test.want.Minor || got.Patch != test.want.Patch {
			t.Fatalf("ParseVersion(%q) = %+v, want %+v", test.input, got, test.want)
		}
		if len(got.Pre) != len(test.want.Pre) {
			t.Fatalf("ParseVersion(%q) prerelease = %v, want %v", test.input, got.Pre, test.want.Pre)
		}
	}
}

func TestParseVersionRejectsMalformedInput(t *testing.T) {
	for _, input := range []string{"", "   ", "v", "1.2.3.4", "1.x.3", "1.2.3-", "1.2.3-rc..1", "-1.2.3"} {
		if _, err := ParseVersion(input); err == nil {
			t.Fatalf("ParseVersion(%q) succeeded, want an error", input)
		}
	}
}

func TestVersionStringRoundTrip(t *testing.T) {
	version, err := ParseVersion("v1.2.3-rc.2+meta")
	if err != nil {
		t.Fatalf("ParseVersion: %v", err)
	}
	if got := version.String(); got != "1.2.3-rc.2" {
		t.Fatalf("String = %q", got)
	}
}
