package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

const pagingNoticeFmt = "[showing lines %d–%d of %d; re-call with offset/limit to page]"

func pagingRoot(t *testing.T, name, content string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func runPagingRead(t *testing.T, root, args string) string {
	t.Helper()
	out, err := (readTool{}).Run(context.Background(), root, json.RawMessage(args))
	if err != nil {
		t.Fatalf("read %s: %v", args, err)
	}
	return out
}

func TestReadFullFileHasNoPagingNotice(t *testing.T) {
	// A trailing newline must not create a phantom line or change the bytes.
	content := "one\ntwo\nthree\n"
	root := pagingRoot(t, "f.txt", content)
	out := runPagingRead(t, root, `{"path":"f.txt"}`)
	if out != content {
		t.Fatalf("full read = %q, want byte-identical %q", out, content)
	}
	if strings.Contains(out, "showing lines") {
		t.Fatalf("full read should not carry a paging notice: %q", out)
	}
}

func TestReadBeyondLimitAppendsPagingNotice(t *testing.T) {
	root := pagingRoot(t, "f.txt", "l1\nl2\nl3\nl4\nl5\n")
	out := runPagingRead(t, root, `{"path":"f.txt","offset":1,"limit":2}`)
	want := "l1\nl2\n" + fmt.Sprintf(pagingNoticeFmt, 1, 2, 5)
	if out != want {
		t.Fatalf("read = %q, want %q", out, want)
	}
}

func TestReadMiddleSliceReportsCorrectRange(t *testing.T) {
	root := pagingRoot(t, "f.txt", "a\nb\nc\nd\ne\nf\n")
	out := runPagingRead(t, root, `{"path":"f.txt","offset":3,"limit":2}`)
	want := "c\nd\n" + fmt.Sprintf(pagingNoticeFmt, 3, 4, 6)
	if out != want {
		t.Fatalf("read = %q, want %q", out, want)
	}
}

func TestReadExactEndHasNoPagingNotice(t *testing.T) {
	// Reaching the final real line (no trailing newline) must stay quiet.
	root := pagingRoot(t, "f.txt", "a\nb\nc\nd\ne")
	out := runPagingRead(t, root, `{"path":"f.txt","offset":4,"limit":2}`)
	if out != "d\ne" {
		t.Fatalf("read = %q, want %q", out, "d\ne")
	}
	if strings.Contains(out, "showing lines") {
		t.Fatalf("exact-end read should not carry a paging notice: %q", out)
	}
}

func TestReadHugeLimitReadsToEndWithoutOverflow(t *testing.T) {
	// start-1+limit used to overflow int for a huge limit and a non-zero
	// offset, making the slice end negative and panicking. Such a limit must
	// behave like "read from offset to the end" instead.
	const content = "a\nb\nc\nd\ne\n"
	root := pagingRoot(t, "f.txt", content)
	for _, huge := range []string{
		"9223372036854775807", // math.MaxInt64
		"9223372036854775806", // MaxInt64-1
		"4611686018427387904", // 1<<62, overflows once start-1 is added
		"999999999999999999",
	} {
		t.Run(huge, func(t *testing.T) {
			args := `{"path":"f.txt","offset":2,"limit":` + huge + `}`
			want := "b\nc\nd\ne\n"
			if got := runPagingRead(t, root, args); got != want {
				t.Fatalf("read with huge limit = %q, want %q", got, want)
			}
		})
	}
}

func TestReadDescriptionHasPagingHintUnderCap(t *testing.T) {
	desc := (readTool{}).Description()
	for _, hint := range []string{"prefer", "shell", "offset", "limit"} {
		if !strings.Contains(desc, hint) {
			t.Fatalf("description %q missing hint %q", desc, hint)
		}
	}
	if len(desc) >= 1024 {
		t.Fatalf("description is %d bytes, want < 1024", len(desc))
	}
	if !utf8.ValidString(desc) {
		t.Fatal("description is not valid UTF-8")
	}
}

func TestReadPagingKeepsUTF8Intact(t *testing.T) {
	root := pagingRoot(t, "f.txt", "你好\n世界\n再见\n朋友\n")
	out := runPagingRead(t, root, `{"path":"f.txt","offset":1,"limit":2}`)
	want := "你好\n世界\n" + fmt.Sprintf(pagingNoticeFmt, 1, 2, 4)
	if out != want {
		t.Fatalf("read = %q, want %q", out, want)
	}
	if !utf8.ValidString(out) || strings.ContainsRune(out, utf8.RuneError) {
		t.Fatalf("read contains broken runes: %q", out)
	}
}

func numberedLines(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = fmt.Sprintf("l%d", i+1)
	}
	return strings.Join(parts, "\n") + "\n"
}

func TestReadGapWindowAroundMiddleLine(t *testing.T) {
	root := pagingRoot(t, "f.txt", numberedLines(10))
	out := runPagingRead(t, root, `{"path":"f.txt","line":5,"before":2,"after":2}`)
	want := "l3\nl4\nl5\nl6\nl7\n" + fmt.Sprintf(pagingNoticeFmt, 3, 7, 10)
	if out != want {
		t.Fatalf("read = %q, want %q", out, want)
	}
}

func TestReadGapWindowClampedAtStartAndEnd(t *testing.T) {
	root := pagingRoot(t, "f.txt", numberedLines(10))
	for _, tt := range []struct {
		name, args, want string
	}{
		{
			name: "near start", args: `{"path":"f.txt","line":1,"before":5,"after":2}`,
			want: "l1\nl2\nl3\n" + fmt.Sprintf(pagingNoticeFmt, 1, 3, 10),
		},
		{
			name: "near end", args: `{"path":"f.txt","line":10,"before":2,"after":5}`,
			want: "l8\nl9\nl10", // reaches the real last line -> no notice
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := runPagingRead(t, root, tt.args); got != tt.want {
				t.Fatalf("read = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReadGapWindowCapsBeforeAndAfter(t *testing.T) {
	// A 500-line file with budgets far above the cap must show exactly 401
	// lines (200 above + target + 200 below), not the whole file.
	root := pagingRoot(t, "f.txt", numberedLines(500))
	out := runPagingRead(t, root, `{"path":"f.txt","line":250,"before":9999,"after":9999}`)
	want := fmt.Sprintf(pagingNoticeFmt, 50, 450, 500)
	if !strings.HasPrefix(out, "l50\n") || !strings.HasSuffix(out, "\nl450\n"+want) {
		t.Fatalf("window not capped to 200 per side: %q", out)
	}
	if got := strings.Count(out, "\n"); got != 401 {
		t.Fatalf("window has %d newlines, want 401 (400 separators + notice lead-in)", got)
	}
}

func TestReadGapWindowKeepsCJKRunesIntact(t *testing.T) {
	root := pagingRoot(t, "f.txt", "一\n二\n三\n四\n五\n")
	out := runPagingRead(t, root, `{"path":"f.txt","line":3,"before":1,"after":1}`)
	want := "二\n三\n四\n" + fmt.Sprintf(pagingNoticeFmt, 2, 4, 5)
	if out != want {
		t.Fatalf("read = %q, want %q", out, want)
	}
	if !utf8.ValidString(out) || strings.ContainsRune(out, utf8.RuneError) {
		t.Fatalf("read contains broken runes: %q", out)
	}
}

func TestReadGapWindowLineWinsOverOffsetLimit(t *testing.T) {
	root := pagingRoot(t, "f.txt", numberedLines(10))
	out := runPagingRead(t, root, `{"path":"f.txt","line":5,"before":0,"after":0,"offset":1,"limit":2}`)
	want := "l5\n" + fmt.Sprintf(pagingNoticeFmt, 5, 5, 10)
	if out != want {
		t.Fatalf("line did not win over offset/limit: got %q, want %q", out, want)
	}
}

func TestReadGapWindowBeyondLastLineIsEmpty(t *testing.T) {
	root := pagingRoot(t, "f.txt", numberedLines(3))
	if got := runPagingRead(t, root, `{"path":"f.txt","line":9,"before":0,"after":0}`); got != "" {
		t.Fatalf("out-of-range window = %q, want empty", got)
	}
}

func TestReadSchemaDocumentsGapWindow(t *testing.T) {
	var schema struct {
		Properties map[string]struct {
			Type        string `json:"type"`
			Description string `json:"description"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal([]byte((readTool{}).Schema()), &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	for _, key := range []string{"path", "offset", "limit", "line", "before", "after"} {
		prop, ok := schema.Properties[key]
		if !ok {
			t.Fatalf("schema missing property %q", key)
		}
		wantType := "string"
		if key != "path" {
			wantType = "integer"
		}
		if prop.Type != wantType {
			t.Errorf("property %q type = %q, want %q", key, prop.Type, wantType)
		}
	}
	if len(schema.Required) != 1 || schema.Required[0] != "path" {
		t.Errorf("required = %v, want [path]", schema.Required)
	}
	desc := (readTool{}).Description()
	for _, hint := range []string{"line", "before", "after", "wins"} {
		if !strings.Contains(desc, hint) {
			t.Errorf("description %q does not document %q", desc, hint)
		}
	}
}
