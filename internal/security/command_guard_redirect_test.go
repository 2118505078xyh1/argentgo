package security

import (
	"strings"
	"testing"
)

// TestFindFileEditingCommandChecksEveryRedirect 覆盖一种重定向绕过：
// 多个 `>` 操作符可以共用一个 shell 词（例如 `>/etc/passwd>/tmp/x`）。
// 旧的 LastIndex 代码只检查最终目标，因此末尾的临时目标会隐藏更早对真实文件的
// 写入。词中的每个重定向都必须独立检查。
func TestFindFileEditingCommandChecksEveryRedirect(t *testing.T) {
	cases := []struct {
		name    string
		command string
		want    string // 违规 token 中期望的子串；"" 表示无匹配
	}{
		{
			name:    "real file before scratch target is still a write",
			command: "echo hi >/etc/passwd>/tmp/scratch.txt",
			want:    "/etc/passwd",
		},
		{
			name:    "real file between two scratch targets",
			command: "echo hi >/tmp/a>/etc/passwd>/tmp/b",
			want:    "/etc/passwd",
		},
		{
			name:    "append redirect before scratch target",
			command: "echo hi >>/etc/passwd>/tmp/scratch.txt",
			want:    "/etc/passwd",
		},
		{
			name:    "all scratch targets stay clear",
			command: "echo hi >/tmp/a>/tmp/b",
			want:    "",
		},
		{
			name:    "fd duplication before a scratch target stays clear",
			command: "echo hi 2>&1>/tmp/scratch.txt",
			want:    "",
		},
		{
			name:    "separate redirection fields still checked",
			command: "echo hi >/etc/passwd >/tmp/scratch.txt",
			want:    "/etc/passwd",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FindFileEditingCommand(tc.command)
			if tc.want == "" {
				if got != "" {
					t.Fatalf("FindFileEditingCommand(%q) = %q, want no match", tc.command, got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("FindFileEditingCommand(%q) = %q, want it to contain %q", tc.command, got, tc.want)
			}
		})
	}
}
