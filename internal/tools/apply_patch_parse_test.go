package tools

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestParseApplyPatchThreeActions(t *testing.T) {
	input := `*** Begin Patch
*** Add File: new.txt
+hello
+world
*** Delete File: old.txt
*** Update File: keep.txt
@@
-old
+new
*** End Patch
`

	actions, err := ParseApplyPatch(input)
	if err != nil {
		t.Fatalf("ParseApplyPatch: %v", err)
	}
	if len(actions) != 3 {
		t.Fatalf("actions len = %d, want 3: %+v", len(actions), actions)
	}

	add := actions[0]
	if add.Kind != "add" || add.Path != "new.txt" {
		t.Errorf("action[0] = %q/%q, want add/new.txt", add.Kind, add.Path)
	}
	if len(add.Hunks) != 1 {
		t.Fatalf("action[0] hunks = %d, want 1", len(add.Hunks))
	}
	if got, want := add.Hunks[0].Lines, []string{"+hello", "+world"}; !reflect.DeepEqual(got, want) {
		t.Errorf("add lines = %q, want %q", got, want)
	}
	if got, want := add.NewText(), "hello\nworld"; got != want {
		t.Errorf("add.NewText() = %q, want %q", got, want)
	}
	if got := add.OldText(); got != "" {
		t.Errorf("add.OldText() = %q, want empty", got)
	}

	del := actions[1]
	if del.Kind != "delete" || del.Path != "old.txt" {
		t.Errorf("action[1] = %q/%q, want delete/old.txt", del.Kind, del.Path)
	}
	if len(del.Hunks) != 0 {
		t.Errorf("action[1] hunks = %d, want 0", len(del.Hunks))
	}

	upd := actions[2]
	if upd.Kind != "update" || upd.Path != "keep.txt" {
		t.Errorf("action[2] = %q/%q, want update/keep.txt", upd.Kind, upd.Path)
	}
	if len(upd.Hunks) != 1 {
		t.Fatalf("action[2] hunks = %d, want 1", len(upd.Hunks))
	}
	if got, want := upd.OldText(), "old"; got != want {
		t.Errorf("update.OldText() = %q, want %q", got, want)
	}
	if got, want := upd.NewText(), "new"; got != want {
		t.Errorf("update.NewText() = %q, want %q", got, want)
	}
}

func TestParseApplyPatchMultipleHunksWithLineNumbers(t *testing.T) {
	input := `*** Begin Patch
*** Update File: f.go
@@ -1,3 +1,4 @@
 package main
-import "fmt"
+import (
+	"fmt"
+)
@@ -10,2 +11,2 @@
-func old() {}
+func new() {}
*** End Patch
`

	actions, err := ParseApplyPatch(input)
	if err != nil {
		t.Fatalf("ParseApplyPatch: %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("actions len = %d, want 1", len(actions))
	}
	hunks := actions[0].Hunks
	if len(hunks) != 2 {
		t.Fatalf("hunks len = %d, want 2", len(hunks))
	}

	if h := hunks[0]; h.OldStart != 1 || h.OldLines != 3 || h.NewStart != 1 || h.NewLines != 4 {
		t.Errorf("hunk[0] numbers = -%d,%d +%d,%d, want -1,3 +1,4", h.OldStart, h.OldLines, h.NewStart, h.NewLines)
	}
	if h := hunks[1]; h.OldStart != 10 || h.OldLines != 2 || h.NewStart != 11 || h.NewLines != 2 {
		t.Errorf("hunk[1] numbers = -%d,%d +%d,%d, want -10,2 +11,2", h.OldStart, h.OldLines, h.NewStart, h.NewLines)
	}

	if got, want := hunks[0].Lines, []string{
		" package main",
		`-import "fmt"`,
		"+import (",
		"+\t\"fmt\"",
		"+)",
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("hunk[0] lines = %q, want %q", got, want)
	}

	// 上下文行同时进入旧/新文本；'-' 只进旧，'+' 只进新。
	if got, want := actions[0].OldText(), "package main\nimport \"fmt\"\nfunc old() {}"; got != want {
		t.Errorf("OldText() = %q, want %q", got, want)
	}
	if got, want := actions[0].NewText(), "package main\nimport (\n\t\"fmt\"\n)\nfunc new() {}"; got != want {
		t.Errorf("NewText() = %q, want %q", got, want)
	}
}

func TestParseApplyPatchHunkHeaderVariants(t *testing.T) {
	input := `*** Begin Patch
*** Update File: a.txt
@@ context hint only
-old
+new
@@ -7 +9 @@
-x
+y
*** End Patch
`

	actions, err := ParseApplyPatch(input)
	if err != nil {
		t.Fatalf("ParseApplyPatch: %v", err)
	}
	hunks := actions[0].Hunks
	if len(hunks) != 2 {
		t.Fatalf("hunks len = %d, want 2", len(hunks))
	}
	// 仅上下文提示的头：行号全 0。
	if h := hunks[0]; h.OldStart != 0 || h.OldLines != 0 || h.NewStart != 0 || h.NewLines != 0 {
		t.Errorf("hunk[0] numbers = -%d,%d +%d,%d, want all zero", h.OldStart, h.OldLines, h.NewStart, h.NewLines)
	}
	// 省略 count 时按 1 计。
	if h := hunks[1]; h.OldStart != 7 || h.OldLines != 1 || h.NewStart != 9 || h.NewLines != 1 {
		t.Errorf("hunk[1] numbers = -%d,%d +%d,%d, want -7,1 +9,1", h.OldStart, h.OldLines, h.NewStart, h.NewLines)
	}
}

func TestParseApplyPatchCJKAndCRLF(t *testing.T) {
	input := "*** Begin Patch\r\n" +
		"*** Update File: 文档/你好.txt\r\n" +
		"@@ -1,2 +1,2 @@\r\n" +
		" 第一行\r\n" +
		"-旧文本\r\n" +
		"+新文本\r\n" +
		"*** End Patch\r\n"

	actions, err := ParseApplyPatch(input)
	if err != nil {
		t.Fatalf("ParseApplyPatch: %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("actions len = %d, want 1", len(actions))
	}
	if got, want := actions[0].Path, "文档/你好.txt"; got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
	if len(actions[0].Hunks) != 1 {
		t.Fatalf("hunks len = %d, want 1", len(actions[0].Hunks))
	}
	if got, want := actions[0].Hunks[0].Lines, []string{" 第一行", "-旧文本", "+新文本"}; !reflect.DeepEqual(got, want) {
		t.Errorf("lines = %q, want %q", got, want)
	}
	for _, ln := range actions[0].Hunks[0].Lines {
		if strings.ContainsRune(ln, '\r') {
			t.Errorf("line %q still contains CR", ln)
		}
	}
	if got, want := actions[0].OldText(), "第一行\n旧文本"; got != want {
		t.Errorf("OldText() = %q, want %q", got, want)
	}
	if got, want := actions[0].NewText(), "第一行\n新文本"; got != want {
		t.Errorf("NewText() = %q, want %q", got, want)
	}
}

func TestParseApplyPatchMalformed(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		wantLine int
	}{
		{
			name: "missing begin",
			input: `*** Update File: a.txt
@@
-x
+y
*** End Patch
`,
			wantLine: 1,
		},
		{
			name:     "empty input",
			input:    "",
			wantLine: 1,
		},
		{
			name: "missing end",
			input: `*** Begin Patch
*** Update File: a.txt
@@
-x
+y
`,
			wantLine: 6,
		},
		{
			name: "unknown directive",
			input: `*** Begin Patch
*** Frobnicate File: a.txt
*** End Patch
`,
			wantLine: 2,
		},
		{
			name: "bad line numbers",
			input: `*** Begin Patch
*** Update File: a.txt
@@ -bad +1 @@
-x
+y
*** End Patch
`,
			wantLine: 3,
		},
		{
			name: "hunk outside file",
			input: `*** Begin Patch
@@
*** End Patch
`,
			wantLine: 2,
		},
		{
			name: "content outside file",
			input: `*** Begin Patch
+stray
*** End Patch
`,
			wantLine: 2,
		},
		{
			name: "empty path",
			input: `*** Begin Patch
*** Add File: 
*** End Patch
`,
			wantLine: 2,
		},
		{
			name: "content after end",
			input: `*** Begin Patch
*** Add File: a.txt
+x
*** End Patch
*** Add File: b.txt
*** End Patch
`,
			wantLine: 5,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			actions, err := ParseApplyPatch(tc.input)
			if err == nil {
				t.Fatalf("ParseApplyPatch() = %+v, want error", actions)
			}
			if !errors.Is(err, ErrMalformedPatch) {
				t.Errorf("err = %v, want errors.Is ErrMalformedPatch", err)
			}
			want := fmt.Sprintf("line %d", tc.wantLine)
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %q, want it to contain %q", err, want)
			}
		})
	}
}

func TestParseApplyPatchEmptyAndSurroundingBlankLines(t *testing.T) {
	input := "\n*** Begin Patch\n*** End Patch\n\n"
	actions, err := ParseApplyPatch(input)
	if err != nil {
		t.Fatalf("ParseApplyPatch: %v", err)
	}
	if len(actions) != 0 {
		t.Errorf("actions len = %d, want 0", len(actions))
	}
}

func TestParseApplyPatchDoesNotMutateInputAndIsStable(t *testing.T) {
	input := `*** Begin Patch
*** Update File: 文档/你好.txt
@@ -1 +1 @@
-旧
+新
*** End Patch
`
	before := input

	first, err := ParseApplyPatch(input)
	if err != nil {
		t.Fatalf("first ParseApplyPatch: %v", err)
	}
	if input != before {
		t.Fatalf("input was mutated:\n got %q\nwant %q", input, before)
	}

	second, err := ParseApplyPatch(input)
	if err != nil {
		t.Fatalf("second ParseApplyPatch: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("parse is not stable:\nfirst  %+v\nsecond %+v", first, second)
	}
}
