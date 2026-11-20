package security

import (
	"strings"
	"testing"
)

// TestFindFileEditingCommandSeesThroughWrapperOptions 验证：包装器（sudo/env/
// nohup/command 等）的选项不能把修改类命令藏起来。`sudo -u root rm file` 过去会
// 被归约成 `-u root rm file`，token 分类器把 `-u` 当作可执行文件，从而让一次
// 明显的删除绕过计划模式守卫与自动审批。
func TestFindFileEditingCommandSeesThroughWrapperOptions(t *testing.T) {
	cases := []struct {
		name    string
		command string
		want    string
	}{
		{"sudo user option", "sudo -u root rm file.txt", "rm"},
		{"sudo no-prompt option", "sudo -n rm file.txt", "rm"},
		{"sudo options then git subcommand", "sudo -u root git push origin main", "git push"},
		{"sudo long option with equals", "sudo --user=root rm file.txt", "rm"},
		{"sudo long option with value", "sudo --user root rm file.txt", "rm"},
		{"env ignore-environment plus assignment", "env -i FOO=1 rm file.txt", "rm"},
		{"env unset option", "env -u HOME mv a b", "mv"},
		{"command portable option", "command -p rm file.txt", "rm"},
		{"nohup end-of-options", "nohup -- rm file.txt", "rm"},
		{"second command wrapped", "ls; sudo -u root rm file.txt", "rm"},
		{"stacked wrappers with options", "sudo env -u root mv a b", "mv"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FindFileEditingCommand(tc.command)
			if !strings.Contains(got, tc.want) {
				t.Fatalf("FindFileEditingCommand(%q) = %q, want it to contain %q", tc.command, got, tc.want)
			}
		})
	}
}

// TestFindFileEditingCommandWrapperOptionsKeepReadOnlyClear 验证：修正选项归约后，
// 只读命令仍然不会被误报。包装器选项值（如用户名）绝不能当作可执行文件。
func TestFindFileEditingCommandWrapperOptionsKeepReadOnlyClear(t *testing.T) {
	for _, command := range []string{
		"sudo -u root ls -la",
		"sudo -n git status",
		"env -i FOO=1 cat file.txt",
		"command -v rm",
		"nohup -- go test ./...",
	} {
		if got := FindFileEditingCommand(command); got != "" {
			t.Fatalf("FindFileEditingCommand(%q) = %q, want no match for a read-only command", command, got)
		}
	}
}

// TestDetectThreatWrapperOptionsStillUpgradesForcedRM 验证：威胁分类器与 token
// 分类器共用同一个归约步骤，因此带选项的 sudo 包装的强制删除仍被提升为 HIGH。
func TestDetectThreatWrapperOptionsStillUpgradesForcedRM(t *testing.T) {
	match := DetectThreat("sudo -u root rm -f important.txt")
	if !match.IsHigh() || match.Category != ThreatForcedRM {
		t.Fatalf("DetectThreat = %+v, want a HIGH-confidence forced-rm match", match)
	}
}
