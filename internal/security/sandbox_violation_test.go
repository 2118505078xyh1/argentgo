package security

import "testing"

func TestClassifySandboxViolationFileSystem(t *testing.T) {
	cases := []struct {
		name   string
		output string
	}{
		{"operation not permitted", "bash: line 1: /etc/shadow: Operation not permitted"},
		{"read-only file system", "touch: /workspace/out.txt: Read-only file system"},
		{"seccomp", "cargo failed: seccomp denied the syscall"},
		{"landlock", "denied by landlock policy"},
		{"sandbox", "sandbox policy blocked the write"},
		{"failed to write file", "failed to write file /workspace/target"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, path, ok := classifySandboxViolation(tc.output)
			if !ok {
				t.Fatalf("%q: 未识别为沙箱违规", tc.output)
			}
			if kind != sandboxViolationFileSystem {
				t.Fatalf("kind = %q, want %q", kind, sandboxViolationFileSystem)
			}
			_ = path
		})
	}
}

func TestClassifySandboxViolationNetwork(t *testing.T) {
	cases := []struct {
		name   string
		output string
	}{
		{"network denied", "curl: network access denied by sandbox"},
		{"proxy blocked", "proxy blocked CONNECT to example.com:443"},
		{"dns denied", "dns lookup denied by network policy"},
		{"中文", "网络访问被沙箱拒绝"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, path, ok := classifySandboxViolation(tc.output)
			if !ok {
				t.Fatalf("%q: 未识别为沙箱违规", tc.output)
			}
			if kind != sandboxViolationNetwork {
				t.Fatalf("kind = %q, want %q", kind, sandboxViolationNetwork)
			}
			if path != "" {
				t.Fatalf("网络违规不应提取文件路径，得到 %q", path)
			}
		})
	}
}

func TestClassifySandboxViolationPermission(t *testing.T) {
	cases := []struct {
		name   string
		output string
	}{
		{"permission denied", "bash: /root/.ssh/id_rsa: Permission denied"},
		{"access denied", "svc: access denied for user"},
		{"EACCES", "open failed: EACCES (os error 13)"},
		{"中文", "权限不足，无法读取文件"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, _, ok := classifySandboxViolation(tc.output)
			if !ok {
				t.Fatalf("%q: 未识别为沙箱违规", tc.output)
			}
			if kind != sandboxViolationPermission {
				t.Fatalf("kind = %q, want %q", kind, sandboxViolationPermission)
			}
		})
	}
}

func TestClassifySandboxViolationExtractsPath(t *testing.T) {
	cases := []struct {
		name   string
		output string
		kind   string
		path   string
	}{
		{
			name:   "posix absolute path",
			output: "bash: /private/tmp/denied: Operation not permitted",
			kind:   sandboxViolationFileSystem,
			path:   "/private/tmp/denied",
		},
		{
			name:   "relative path",
			output: "write ./src/main.go: Read-only file system",
			kind:   sandboxViolationFileSystem,
			path:   "./src/main.go",
		},
		{
			name:   "permission path",
			output: "cat: /etc/shadow: Permission denied",
			kind:   sandboxViolationPermission,
			path:   "/etc/shadow",
		},
		{
			name:   "windows drive path",
			output: `del C:\Windows\System32\drivers\etc\hosts: Permission denied`,
			kind:   sandboxViolationPermission,
			path:   `C:\Windows\System32\drivers\etc\hosts`,
		},
		{
			name:   "quoted path",
			output: `bash: "/tmp/secret key": Operation not permitted`,
			kind:   sandboxViolationFileSystem,
			path:   "/tmp/secret key",
		},
		{
			name:   "no path in output",
			output: "Permission denied",
			kind:   sandboxViolationPermission,
			path:   "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, path, ok := classifySandboxViolation(tc.output)
			if !ok {
				t.Fatalf("%q: 未识别为沙箱违规", tc.output)
			}
			if kind != tc.kind {
				t.Fatalf("kind = %q, want %q", kind, tc.kind)
			}
			if path != tc.path {
				t.Fatalf("path = %q, want %q", path, tc.path)
			}
		})
	}
}

func TestClassifySandboxViolationCJK(t *testing.T) {
	// CJK 与非 ASCII 字符出现在路径及标记之前时，提取必须保持字节完整，
	// 不能因大小写折叠或切片偏移而损坏多字节字符。
	cases := []struct {
		name   string
		output string
		kind   string
		path   string
	}{
		{
			name:   "CJK path before marker",
			output: "bash: /private/tmp/中文-目录/文件: Operation not permitted",
			kind:   sandboxViolationFileSystem,
			path:   "/private/tmp/中文-目录/文件",
		},
		{
			name:   "fullwidth colon marker",
			output: "bash：/tmp/机密文件：权限被拒绝",
			kind:   sandboxViolationPermission,
			path:   "/tmp/机密文件",
		},
		{
			name:   "unicode letter before marker",
			output: "bash: /private/tmp/İ-denied: Operation not permitted",
			kind:   sandboxViolationFileSystem,
			path:   "/private/tmp/İ-denied",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, path, ok := classifySandboxViolation(tc.output)
			if !ok {
				t.Fatalf("%q: 未识别为沙箱违规", tc.output)
			}
			if kind != tc.kind {
				t.Fatalf("kind = %q, want %q", kind, tc.kind)
			}
			if path != tc.path {
				t.Fatalf("path = %q, want %q", path, tc.path)
			}
		})
	}
}

func TestClassifySandboxViolationEmptyAndIrrelevant(t *testing.T) {
	for _, output := range []string{
		"",
		"   \n\t ",
		"hello 世界",
		"command not found",
		"compilation error: undefined symbol",
		"connection refused by remote host",
	} {
		kind, path, ok := classifySandboxViolation(output)
		if ok || kind != "" || path != "" {
			t.Fatalf("%q: got (%q, %q, %v), want 未识别", output, kind, path, ok)
		}
	}
}

func TestClassifySandboxViolationNetworkTakesPrecedence(t *testing.T) {
	// 同时出现网络上下文与泛化的 sandbox 标记时必须归为网络违规。
	kind, _, ok := classifySandboxViolation("sandbox: network access denied for example.com")
	if !ok {
		t.Fatal("未识别为沙箱违规")
	}
	if kind != sandboxViolationNetwork {
		t.Fatalf("kind = %q, want %q", kind, sandboxViolationNetwork)
	}
}
