package security

import (
	"strings"
	"testing"
)

func TestDangerousCommandDetail(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want []DangerousCommandKind
	}{
		{"forced rm", "rm -f file.txt", []DangerousCommandKind{DangerForcedRm}},
		{"recursive rm", "rm -r dir", []DangerousCommandKind{DangerRecursiveRm}},
		{"forced recursive rm", "rm -rf dir", []DangerousCommandKind{DangerForcedRm, DangerRecursiveRm}},
		{"long force", "rm --force file", []DangerousCommandKind{DangerForcedRm}},
		{"windows del", "del /F file", []DangerousCommandKind{DangerForcedRm}},
		{"interpreted python", "python -c 'import os'", []DangerousCommandKind{DangerInterpreter}},
		{"node eval", "node -e 'process.exit()'", []DangerousCommandKind{DangerInterpreter}},
		{"shell wrapper", "bash -c 'echo hi'", []DangerousCommandKind{DangerShellWrapper}},
		{"powershell encoded", "powershell -EncodedCommand ZWNobyBoaQ==", []DangerousCommandKind{DangerEncodedExec}},
		{"sudo", "sudo rm file", []DangerousCommandKind{DangerPrivilege}},
		{"disk wipe", "mkfs.ext4 /dev/sda", []DangerousCommandKind{DangerDiskWipe}},
		{"benign", "git status", nil},
		{"plain rm", "rm file", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DangerousCommandDetail(tc.cmd)
			if len(got) != len(tc.want) {
				t.Fatalf("detail = %+v, want kinds %v", got, tc.want)
			}
			for i, w := range tc.want {
				if got[i].Kind != w {
					t.Errorf("detail[%d].Kind = %q, want %q", i, got[i].Kind, w)
				}
			}
		})
	}
}

func TestDetailSummary(t *testing.T) {
	got := DetailSummary(DangerousCommandDetail("rm -rf build"))
	if !strings.Contains(got, "强制删除") || !strings.Contains(got, "递归删除") {
		t.Fatalf("summary = %q", got)
	}
	if DetailSummary(nil) != "" {
		t.Fatal("empty matches should produce empty summary")
	}
}

func TestNormalizeCommandNameCom(t *testing.T) {
	cases := map[string]string{
		"foo.com":                        "foo",
		"C:\\Windows\\System32\\cmd.exe": "cmd",
		"/usr/bin/rm":                    "rm",
		"Remove-Item.ps1":                "remove-item",
		"foo.bat":                        "foo",
	}
	for input, want := range cases {
		if got := normalizeCommandName(input); got != want {
			t.Errorf("normalizeCommandName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestDangerousDetailNoDuplicateKinds(t *testing.T) {
	// 多个 rm -f 只报一次 forced-rm（去重）。
	got := DangerousCommandDetail("rm -f a; rm -f b")
	count := 0
	for _, m := range got {
		if m.Kind == DangerForcedRm {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("forced-rm reported %d times, want 1: %+v", count, got)
	}
}

func TestDangerousDetailPlatform(t *testing.T) {
	got := DangerousCommandDetail("del /F x")
	if len(got) != 1 || got[0].Platform != "windows" {
		t.Fatalf("windows platform not detected: %+v", got)
	}
	got = DangerousCommandDetail("rm -f x")
	if len(got) != 1 || got[0].Platform != "posix" {
		t.Fatalf("posix platform not detected: %+v", got)
	}
}
