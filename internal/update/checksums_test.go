package update

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseChecksumsAcceptsCanonicalForms(t *testing.T) {
	const hashA = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	const hashB = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	text := strings.Join([]string{
		"# released checksums",
		"",
		hashA + "  argent_1.2.3_windows_amd64.zip",
		hashB + " *argent_1.2.3_linux_amd64.tar.gz",
		"  " + hashA + "  ./sub/argent.exe  ",   // leading/trailing whitespace, ./ prefix
		strings.ToUpper(hashB) + "  ARGENT.EXE", // uppercase digest normalised
	}, "\n")

	got, err := ParseChecksums(text)
	if err != nil {
		t.Fatalf("ParseChecksums: %v", err)
	}
	want := []ChecksumEntry{
		{Hash: hashA, Name: "argent_1.2.3_windows_amd64.zip"},
		{Hash: hashB, Name: "argent_1.2.3_linux_amd64.tar.gz"},
		{Hash: hashA, Name: "sub/argent.exe"},
		{Hash: hashB, Name: "ARGENT.EXE"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseChecksums =\n%+v\nwant\n%+v", got, want)
	}
}

func TestParseChecksumsRejectsMalformedLines(t *testing.T) {
	const valid = "0000000000000000000000000000000000000000000000000000000000000000  good.bin"
	cases := []struct {
		name     string
		text     string
		wantLine string
	}{
		{"no separator", "abcd", "line 1"},
		{"single space", "0000 0000", "line 1"},
		{"short digest", "abc  file.bin", "line 1"},
		{"non hex digest", strings.Repeat("z", 64) + "  file.bin", "line 1"},
		{"empty name", strings.Repeat("a", 64) + "  ", "line 1"},
		{"bad second line", valid + "\n" + strings.Repeat("b", 64) + " *", "line 2"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseChecksums(test.text)
			if err == nil {
				t.Fatalf("ParseChecksums(%q) succeeded, got %+v", test.text, got)
			}
			if !strings.Contains(err.Error(), test.wantLine) {
				t.Fatalf("error = %v, want it to mention %q", err, test.wantLine)
			}
		})
	}
}

func TestParseChecksumsHandlesCRLFAndEmptyInput(t *testing.T) {
	const hash = "1111111111111111111111111111111111111111111111111111111111111111"
	text := "# comment\r\n\r\n" + hash + "  a.bin\r\n"
	got, err := ParseChecksums(text)
	if err != nil {
		t.Fatalf("ParseChecksums: %v", err)
	}
	if len(got) != 1 || got[0].Name != "a.bin" {
		t.Fatalf("got %+v", got)
	}
	empty, err := ParseChecksums("")
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty input = %+v, %v", empty, err)
	}
}

func TestParseChecksumsIsPure(t *testing.T) {
	text := "2222222222222222222222222222222222222222222222222222222222222222  a.bin"
	first, err := ParseChecksums(text)
	if err != nil {
		t.Fatal(err)
	}
	first[0].Name = "mutated"
	second, err := ParseChecksums(text)
	if err != nil {
		t.Fatal(err)
	}
	if second[0].Name != "a.bin" {
		t.Fatalf("second parse was affected by mutating the first: %+v", second)
	}
}

func TestDiagnoseChecksumsMatchesAndMissing(t *testing.T) {
	const hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	assets := []Asset{
		{Name: "argent_windows_amd64.zip"},
		{Name: "argent_linux_amd64.tar.gz"},
	}
	entries := []ChecksumEntry{{Hash: hashA, Name: "argent_windows_amd64.zip"}}
	want := ChecksumDiagnosis{
		Matched: []MatchedChecksum{{Name: "argent_windows_amd64.zip", Hash: hashA}},
		Missing: []string{"argent_linux_amd64.tar.gz"},
	}
	if got := DiagnoseChecksums(entries, assets); !reflect.DeepEqual(got, want) {
		t.Fatalf("DiagnoseChecksums =\n%+v\nwant\n%+v", got, want)
	}
}

func TestDiagnoseChecksumsReportsOrphansConflictsAndCase(t *testing.T) {
	const hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const hashC = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	assets := []Asset{{Name: "Argent.EXE"}}
	entries := []ChecksumEntry{
		{Hash: hashA, Name: "argent.exe"},
		{Hash: hashB, Name: "ARGENT.EXE"},
		{Hash: hashC, Name: "ghost.bin"},
	}
	want := ChecksumDiagnosis{
		Missing:   []string{"Argent.EXE"},
		Orphans:   []string{"ghost.bin"},
		Conflicts: []string{"argent.exe"},
	}
	if got := DiagnoseChecksums(entries, assets); !reflect.DeepEqual(got, want) {
		t.Fatalf("DiagnoseChecksums =\n%+v\nwant\n%+v", got, want)
	}
}

func TestDiagnoseChecksumsIsDeterministicAndPure(t *testing.T) {
	const hash = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	assets := []Asset{{Name: "a.bin"}, {Name: "b.bin"}}
	entries := []ChecksumEntry{{Hash: hash, Name: "a.bin"}}
	first := DiagnoseChecksums(entries, assets)
	for i := 0; i < 5; i++ {
		if got := DiagnoseChecksums(entries, assets); !reflect.DeepEqual(got, first) {
			t.Fatalf("run %d =\n%+v\nwant\n%+v", i, got, first)
		}
	}
	entries[0].Name = "mutated"
	assets[0].Name = "mutated"
	if first.Matched[0].Name != "a.bin" {
		t.Fatal("DiagnoseChecksums retained references to its inputs")
	}
}

func TestDiagnoseChecksumsEmptyInputs(t *testing.T) {
	if got := DiagnoseChecksums(nil, nil); len(got.Matched) != 0 ||
		len(got.Missing) != 0 || len(got.Orphans) != 0 || len(got.Conflicts) != 0 {
		t.Fatalf("DiagnoseChecksums(nil, nil) = %+v", got)
	}
	if got := DiagnoseChecksums(nil, []Asset{{Name: "solo.bin"}}); !reflect.DeepEqual(got.Missing, []string{"solo.bin"}) {
		t.Fatalf("no manifest should mark every asset missing: %+v", got)
	}
}
