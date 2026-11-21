package eval

import (
	"reflect"
	"testing"
)

// TestIsolatedEnv 覆盖白名单过滤的核心规则：只保留 KEY= 前缀命中的变量、
// 丢弃 SECRET_TOKEN 之类的敏感项、去重、保持 base 顺序、大小写精确匹配。
func TestIsolatedEnv(t *testing.T) {
	tests := []struct {
		name  string
		base  []string
		allow []string
		want  []string
	}{
		{
			name: "keeps allowlisted keys and drops secrets",
			base: []string{
				"PATH=/usr/bin",
				"SECRET_TOKEN=abc123",
				"HOME=/home/u",
				"AWS_SECRET_ACCESS_KEY=wJalrXUtn",
				"TMPDIR=/tmp",
			},
			allow: []string{"PATH", "HOME", "TMPDIR"},
			want:  []string{"PATH=/usr/bin", "HOME=/home/u", "TMPDIR=/tmp"},
		},
		{
			name: "dedupes repeated keys keeping the first occurrence",
			base: []string{
				"PATH=/first",
				"HOME=/h",
				"PATH=/second",
				"TMPDIR=/tmp",
				"PATH=/third",
			},
			allow: []string{"PATH", "HOME", "TMPDIR"},
			want:  []string{"PATH=/first", "HOME=/h", "TMPDIR=/tmp"},
		},
		{
			name: "only exact KEY= prefix matches, not a substring",
			base: []string{
				"PATH=/ok",
				"MYPATH=/no",
				"PATH_EXTRA=/no",
				"XPATH=/no",
			},
			allow: []string{"PATH"},
			want:  []string{"PATH=/ok"},
		},
		{
			name: "matching is case sensitive",
			base: []string{
				"PATH=/upper",
				"path=/lower",
				`SystemRoot=C:\Windows`,
				`systemroot=c:\lower`,
			},
			allow: []string{"PATH", "SystemRoot"},
			want:  []string{"PATH=/upper", `SystemRoot=C:\Windows`},
		},
		{
			name:  "empty value is preserved",
			base:  []string{"EMPTY=", "PATH=/x"},
			allow: []string{"EMPTY", "PATH"},
			want:  []string{"EMPTY=", "PATH=/x"},
		},
		{
			name:  "malformed entries without = are ignored",
			base:  []string{"MALFORMED", "PATH=/x"},
			allow: []string{"MALFORMED", "PATH"},
			want:  []string{"PATH=/x"},
		},
		{
			name:  "empty allow returns empty",
			base:  []string{"PATH=/x", "SECRET_TOKEN=s"},
			allow: []string{},
			want:  []string{},
		},
		{
			name:  "nil allow returns empty",
			base:  []string{"PATH=/x"},
			allow: nil,
			want:  []string{},
		},
		{
			name:  "nil base returns empty",
			base:  nil,
			allow: []string{"PATH"},
			want:  []string{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := IsolatedEnv(tc.base, tc.allow)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("IsolatedEnv() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestIsolatedEnvDoesNotMutateInputs 验证纯函数承诺：调用前后 base 与 allow 必须逐项不变。
func TestIsolatedEnvDoesNotMutateInputs(t *testing.T) {
	base := []string{"PATH=/a", "SECRET_TOKEN=s", "PATH=/b", "HOME=/h"}
	allow := []string{"PATH", "HOME"}

	baseBefore := append([]string(nil), base...)
	allowBefore := append([]string(nil), allow...)

	got := IsolatedEnv(base, allow)

	if !reflect.DeepEqual(got, []string{"PATH=/a", "HOME=/h"}) {
		t.Fatalf("IsolatedEnv() = %q, want [PATH=/a HOME=/h]", got)
	}
	if !reflect.DeepEqual(base, baseBefore) {
		t.Fatalf("base was mutated: got %q, want %q", base, baseBefore)
	}
	if !reflect.DeepEqual(allow, allowBefore) {
		t.Fatalf("allow was mutated: got %q, want %q", allow, allowBefore)
	}
}

// TestDefaultIsolatedAllowlist 验证跨平台最小基线：Windows 必含 SystemRoot 等关键项，
// Unix 含 PATH/HOME/TMPDIR，未知 goos 按 Unix 处理，且列表自身无重复。
func TestDefaultIsolatedAllowlist(t *testing.T) {
	windowsWant := []string{"SystemRoot", "TEMP", "TMP", "PATH", "USERPROFILE"}
	unixWant := []string{"PATH", "HOME", "TMPDIR"}

	tests := []struct {
		name string
		goos string
		want []string
	}{
		{name: "windows", goos: "windows", want: windowsWant},
		{name: "windows is case insensitive", goos: "WiNdOwS", want: windowsWant},
		{name: "linux", goos: "linux", want: unixWant},
		{name: "darwin", goos: "darwin", want: unixWant},
		{name: "unknown falls back to unix", goos: "plan9", want: unixWant},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DefaultIsolatedAllowlist(tc.goos)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("DefaultIsolatedAllowlist(%q) = %q, want %q", tc.goos, got, tc.want)
			}
			if duplicated(got) {
				t.Fatalf("DefaultIsolatedAllowlist(%q) contains duplicates: %q", tc.goos, got)
			}
		})
	}
}

// TestDefaultIsolatedAllowlistHasNoSecrets 确保基线白名单不含凭据或可注入的标志变量。
func TestDefaultIsolatedAllowlistHasNoSecrets(t *testing.T) {
	forbidden := map[string]bool{
		"SECRET_TOKEN":          true,
		"AWS_SECRET_ACCESS_KEY": true,
		"GOFLAGS":               true,
	}
	for _, goos := range []string{"windows", "linux"} {
		for _, key := range DefaultIsolatedAllowlist(goos) {
			if forbidden[key] {
				t.Errorf("DefaultIsolatedAllowlist(%q) unexpectedly allows %q", goos, key)
			}
		}
	}
}

// TestIsolatedEnvWithDefaultAllowlist 把两个函数串起来验证蓝图：对典型的含凭据环境，
// 过滤结果只剩基线变量，敏感项与 GOFLAGS 都被剔除。
func TestIsolatedEnvWithDefaultAllowlist(t *testing.T) {
	tests := []struct {
		name string
		goos string
		base []string
		want []string
	}{
		{
			name: "windows",
			goos: "windows",
			base: []string{
				`SystemRoot=C:\Windows`,
				`TEMP=C:\Temp`,
				`TMP=C:\Temp`,
				`PATH=C:\bin`,
				`USERPROFILE=C:\Users\u`,
				`APPDATA=C:\AppData`,
				"SECRET_TOKEN=s3cr3t",
				"GOFLAGS=-toolexec=evil",
			},
			want: []string{
				`SystemRoot=C:\Windows`,
				`TEMP=C:\Temp`,
				`TMP=C:\Temp`,
				`PATH=C:\bin`,
				`USERPROFILE=C:\Users\u`,
			},
		},
		{
			name: "unix",
			goos: "linux",
			base: []string{
				"PATH=/usr/bin",
				"HOME=/home/u",
				"TMPDIR=/tmp",
				"SECRET_TOKEN=s3cr3t",
				"AWS_SECRET_ACCESS_KEY=wJalrXUtn",
				"GOFLAGS=-toolexec=evil",
			},
			want: []string{"PATH=/usr/bin", "HOME=/home/u", "TMPDIR=/tmp"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := IsolatedEnv(tc.base, DefaultIsolatedAllowlist(tc.goos))
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("isolated env = %q, want %q", got, tc.want)
			}
		})
	}
}

// duplicated 报告切片中是否有重复字符串，用于检查白名单列表自身。
func duplicated(items []string) bool {
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if _, ok := seen[item]; ok {
			return true
		}
		seen[item] = struct{}{}
	}
	return false
}
