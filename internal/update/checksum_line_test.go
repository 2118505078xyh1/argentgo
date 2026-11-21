package update

import (
	"strings"
	"testing"
)

const lineDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
const lineDigestUpper = "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF"

func TestChecksumLineMatches(t *testing.T) {
	cases := []struct {
		name     string
		line     string
		wantName string
		wantHash string
		wantOK   bool
	}{
		{
			name:     "valid line with matching name",
			line:     lineDigest + "  argent_1.2.3_windows_amd64.zip",
			wantName: "argent_1.2.3_windows_amd64.zip",
			wantHash: lineDigest,
			wantOK:   true,
		},
		{
			name:     "uppercase digest normalised to lowercase",
			line:     lineDigestUpper + "  argent.exe",
			wantName: "argent.exe",
			wantHash: lineDigest,
			wantOK:   true,
		},
		{
			name:     "name mismatch",
			line:     lineDigest + "  argent.exe",
			wantName: "other.exe",
			wantOK:   false,
		},
		{
			name:     "name is only a substring",
			line:     lineDigest + "  argent_1.2.3_windows_amd64.zip",
			wantName: "argent",
			wantOK:   false,
		},
		{
			name:     "cjk filename matching",
			line:     lineDigest + "  发布包-中文.zip",
			wantName: "发布包-中文.zip",
			wantHash: lineDigest,
			wantOK:   true,
		},
		{
			name:     "cjk filename mismatch",
			line:     lineDigest + "  发布包-中文.zip",
			wantName: "发布包-日文.zip",
			wantOK:   false,
		},
		{
			name:     "filename with internal double spaces",
			line:     lineDigest + "  my  release.zip",
			wantName: "my  release.zip",
			wantHash: lineDigest,
			wantOK:   true,
		},
		{
			name:     "cjk filename with internal double spaces",
			line:     lineDigest + "  我的  发布包.zip",
			wantName: "我的  发布包.zip",
			wantHash: lineDigest,
			wantOK:   true,
		},
		{
			name:     "non hex character in digest",
			line:     strings.Repeat("z", 64) + "  argent.exe",
			wantName: "argent.exe",
			wantOK:   false,
		},
		{
			name:     "digest too short",
			line:     strings.Repeat("a", 63) + "  argent.exe",
			wantName: "argent.exe",
			wantOK:   false,
		},
		{
			name:     "digest too long",
			line:     strings.Repeat("a", 65) + "  argent.exe",
			wantName: "argent.exe",
			wantOK:   false,
		},
		{
			name:     "single space separator",
			line:     lineDigest + " argent.exe",
			wantName: "argent.exe",
			wantOK:   false,
		},
		{
			name:     "binary mode separator rejected",
			line:     lineDigest + " *argent.exe",
			wantName: "argent.exe",
			wantOK:   false,
		},
		{
			name:     "tab separator rejected",
			line:     lineDigest + "\targent.exe",
			wantName: "argent.exe",
			wantOK:   false,
		},
		{
			name:     "empty filename rejected",
			line:     lineDigest + "  ",
			wantName: "",
			wantOK:   false,
		},
		{
			name:     "leading whitespace rejected",
			line:     " " + lineDigest + "  argent.exe",
			wantName: "argent.exe",
			wantOK:   false,
		},
		{
			name:     "trailing carriage return is not trimmed",
			line:     lineDigest + "  argent.exe\r",
			wantName: "argent.exe",
			wantOK:   false,
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			hash, ok := checksumLineMatches(test.line, test.wantName)
			if ok != test.wantOK {
				t.Fatalf("checksumLineMatches(%q, %q) ok = %v, want %v (hash %q)",
					test.line, test.wantName, ok, test.wantOK, hash)
			}
			if hash != test.wantHash {
				t.Fatalf("checksumLineMatches(%q, %q) hash = %q, want %q",
					test.line, test.wantName, hash, test.wantHash)
			}
		})
	}
}

// TestChecksumLineMatchesIsStable 验证对相同输入重复调用得到完全一致的结果，
// 并且不会修改输入字符串。
func TestChecksumLineMatchesIsStable(t *testing.T) {
	line := lineDigest + "  发布包-中文.zip"
	wantName := "发布包-中文.zip"
	firstHash, firstOK := checksumLineMatches(line, wantName)
	for i := 0; i < 5; i++ {
		hash, ok := checksumLineMatches(line, wantName)
		if hash != firstHash || ok != firstOK || !ok {
			t.Fatalf("run %d = (%q, %v), want (%q, %v)", i, hash, ok, firstHash, firstOK)
		}
	}
	if line != lineDigest+"  发布包-中文.zip" || wantName != "发布包-中文.zip" {
		t.Fatal("checksumLineMatches modified its inputs")
	}
}
