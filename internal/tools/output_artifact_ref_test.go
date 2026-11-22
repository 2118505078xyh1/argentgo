package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFormatArtifactRefBasic(t *testing.T) {
	for _, tc := range []struct {
		name string
		ref  OutputArtifactRef
		want string
	}{
		{
			name: "path and byte count",
			ref:  OutputArtifactRef{Path: "/tmp/tool-output-abc.log", Bytes: 12345},
			want: "[完整输出已保存: /tmp/tool-output-abc.log (12345 bytes)]",
		},
		{
			name: "zero bytes",
			ref:  OutputArtifactRef{Path: "a.log", Bytes: 0},
			want: "[完整输出已保存: a.log (0 bytes)]",
		},
		{
			name: "CJK path kept verbatim including backslashes",
			ref:  OutputArtifactRef{Path: "D:\\日志\\工具输出.log", Bytes: 7},
			want: `[完整输出已保存: D:\日志\工具输出.log (7 bytes)]`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := FormatArtifactRef(tc.ref)
			if got != tc.want {
				t.Fatalf("FormatArtifactRef(%+v) = %q, want %q", tc.ref, got, tc.want)
			}
		})
	}
}

func TestFormatArtifactRefWithPreview(t *testing.T) {
	ref := OutputArtifactRef{Path: "/tmp/out.log", Bytes: 42, Preview: "hello world"}
	want := "[完整输出已保存: /tmp/out.log (42 bytes)] hello world"
	if got := FormatArtifactRef(ref); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestFormatArtifactRefRejectsInvalid(t *testing.T) {
	for _, tc := range []struct {
		name string
		ref  OutputArtifactRef
	}{
		{"empty path", OutputArtifactRef{Path: "", Bytes: 1}},
		{"whitespace path", OutputArtifactRef{Path: "   \t ", Bytes: 1}},
		{"negative bytes", OutputArtifactRef{Path: "a.log", Bytes: -1}},
		{"negative bytes with preview", OutputArtifactRef{Path: "a.log", Bytes: -9, Preview: "x"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := FormatArtifactRef(tc.ref); got != "" {
				t.Fatalf("invalid ref should format to empty string, got %q", got)
			}
		})
	}
}

func TestFormatArtifactRefIsSingleLine(t *testing.T) {
	ref := OutputArtifactRef{
		Path:    "a\nb\rc\td\x00e\u2028f\u2029g\x7fh",
		Bytes:   1,
		Preview: "line1\nline2\rend\ttab",
	}
	got := FormatArtifactRef(ref)
	// 原始控制字符必须全部被转义，结果里不应再出现换行/回车/行分隔符。
	for _, bad := range []rune{'\n', '\r', '\u2028', '\u2029', 0x00, 0x7f} {
		if strings.ContainsRune(got, bad) {
			t.Fatalf("output contains unescaped control rune %U: %q", bad, got)
		}
	}
	want := `[完整输出已保存: a\nb\rc\td\x00e\u2028f\u2029g\x7fh (1 bytes)] line1\nline2\rend\ttab`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestFormatArtifactRefPreviewTruncationASCII(t *testing.T) {
	preview := strings.Repeat("x", 200)
	wantSuffix := strings.Repeat("x", artifactRefPreviewMaxBytes-len(artifactRefEllipsis)) + artifactRefEllipsis
	got := FormatArtifactRef(OutputArtifactRef{Path: "a.log", Bytes: 9, Preview: preview})

	want := "[完整输出已保存: a.log (9 bytes)] " + wantSuffix
	if got != want {
		t.Fatalf("truncated preview mismatch:\n got %q\nwant %q", got, want)
	}
	if len(wantSuffix) != artifactRefPreviewMaxBytes {
		t.Fatalf("preview suffix length = %d, want %d", len(wantSuffix), artifactRefPreviewMaxBytes)
	}
	if !utf8.ValidString(got) {
		t.Fatal("formatted text is not valid UTF-8")
	}
}

func TestFormatArtifactRefPreviewTruncationCJK(t *testing.T) {
	// 40 个「中」= 120 字节，超过 96 字节上界；预算 93 字节恰好容纳 31 个 rune。
	preview := strings.Repeat("中", 40)
	wantSuffix := strings.Repeat("中", 31) + artifactRefEllipsis
	got := FormatArtifactRef(OutputArtifactRef{Path: "日志.log", Bytes: 120, Preview: preview})

	want := "[完整输出已保存: 日志.log (120 bytes)] " + wantSuffix
	if got != want {
		t.Fatalf("CJK truncated preview mismatch:\n got %q\nwant %q", got, want)
	}
	if !utf8.ValidString(got) {
		t.Fatal("CJK preview was cut mid-rune")
	}
	if len(wantSuffix) != artifactRefPreviewMaxBytes {
		t.Fatalf("preview suffix length = %d, want %d", len(wantSuffix), artifactRefPreviewMaxBytes)
	}
	// 31 个汉字 + 3 个 ASCII 点 = 34 个 rune，确认按 rune 计数正确。
	if n := utf8.RuneCountInString(wantSuffix); n != 34 {
		t.Fatalf("preview rune count = %d, want 34", n)
	}
}

func TestArtifactRefFromSpillExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tool-output-0123456789abcdef.log")
	content := "hello\n世界"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	ref, ok := ArtifactRefFromSpill(path)
	if !ok {
		t.Fatal("existing readable spill file reported as missing")
	}
	if ref.Path != path {
		t.Fatalf("Path = %q, want %q", ref.Path, path)
	}
	if ref.Bytes != int64(len(content)) {
		t.Fatalf("Bytes = %d, want %d", ref.Bytes, len(content))
	}
	if ref.Preview != "" {
		t.Fatalf("Preview should be left empty for caller to fill, got %q", ref.Preview)
	}
}

func TestArtifactRefFromSpillMissingOrInvalid(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.log")
	for _, tc := range []struct {
		name string
		path string
	}{
		{"missing file", missing},
		{"empty path", ""},
		{"whitespace path", "  "},
		{"directory is not a regular file", t.TempDir()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref, ok := ArtifactRefFromSpill(tc.path)
			if ok {
				t.Fatalf("expected false for %q, got true with %+v", tc.path, ref)
			}
			if ref != (OutputArtifactRef{}) {
				t.Fatalf("expected zero-value ref on failure, got %+v", ref)
			}
		})
	}
}

func TestArtifactRefDeterministic(t *testing.T) {
	ref := OutputArtifactRef{Path: "/tmp/日志.log", Bytes: 2048, Preview: strings.Repeat("世界", 60)}
	if a, b := FormatArtifactRef(ref), FormatArtifactRef(ref); a != b {
		t.Fatalf("FormatArtifactRef is not deterministic:\n%q\n%q", a, b)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "tool-output-abcdef0123456789.log")
	if err := os.WriteFile(path, []byte("deterministic"), 0o600); err != nil {
		t.Fatal(err)
	}
	a, okA := ArtifactRefFromSpill(path)
	b, okB := ArtifactRefFromSpill(path)
	if !okA || !okB || a != b {
		t.Fatalf("ArtifactRefFromSpill is not deterministic: (%+v,%v) vs (%+v,%v)", a, okA, b, okB)
	}
}

func TestArtifactRefDoesNotMutateInput(t *testing.T) {
	ref := OutputArtifactRef{Path: "/tmp/keep.log", Bytes: 15, Preview: "keep me"}
	before := ref
	_ = FormatArtifactRef(ref)
	if ref != before {
		t.Fatalf("FormatArtifactRef mutated its input: before %+v, after %+v", before, ref)
	}

	path := "/tmp/keep-path.log"
	_, _ = ArtifactRefFromSpill(path)
	if path != "/tmp/keep-path.log" {
		t.Fatalf("ArtifactRefFromSpill mutated path argument: %q", path)
	}
}
