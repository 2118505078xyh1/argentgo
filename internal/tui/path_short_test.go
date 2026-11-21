package tui

import (
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestShortenPathWithHome 覆盖 smartShortenPath 纯函数内核的各条分支。
func TestShortenPathWithHome(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		home     string
		maxWidth int
		want     string
	}{
		{
			name:     "非正宽度返回空串",
			path:     "/a/b/c.go",
			maxWidth: 0,
			want:     "",
		},
		{
			name:     "负宽度返回空串",
			path:     "/a/b/c.go",
			maxWidth: -3,
			want:     "",
		},
		{
			name:     "空路径返回空串",
			path:     "",
			maxWidth: 20,
			want:     "",
		},
		{
			name:     "宽度足够时原样返回",
			path:     "/usr/local/bin/tool",
			maxWidth: 100,
			want:     "/usr/local/bin/tool",
		},
		{
			name:     "主目录本身替换为波浪号",
			path:     "/home/alice",
			home:     "/home/alice",
			maxWidth: 100,
			want:     "~",
		},
		{
			name:     "主目录前缀替换为波浪号",
			path:     "/home/alice/foo/bar",
			home:     "/home/alice",
			maxWidth: 100,
			want:     "~/foo/bar",
		},
		{
			name:     "同前缀的其它目录不替换",
			path:     "/home/alice2/foo",
			home:     "/home/alice",
			maxWidth: 100,
			want:     "/home/alice2/foo",
		},
		{
			name:     "Windows 反斜杠主目录替换",
			path:     `C:\Users\bob\proj\x.go`,
			home:     `C:\Users\bob`,
			maxWidth: 100,
			want:     `~\proj\x.go`,
		},
		{
			name:     "混合分隔符也能替换并保留路径分隔符",
			path:     `C:/Users/bob/proj/x.go`,
			home:     `C:\Users\bob`,
			maxWidth: 100,
			want:     `~/proj/x.go`,
		},
		{
			name:     "中间组件缩成首字并保留末两段与扩展名",
			path:     "/home/alice/projects/argent-go/internal/tui/render.go",
			home:     "/home/alice",
			maxWidth: 22,
			want:     "~/p/a/i/tui/render.go",
		},
		{
			name:     "缩写后仍超宽则丢弃中间组件",
			path:     "/home/alice/projects/argent-go/internal/tui/render.go",
			home:     "/home/alice",
			maxWidth: 17,
			want:     "~/…/tui/render.go",
		},
		{
			name:     "根目录路径缩写",
			path:     "/var/log/application/services/api/server/main.go",
			home:     "/home/alice",
			maxWidth: 22,
			want:     "/…/s/a/server/main.go",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shortenPathWithHome(tt.path, tt.home, tt.maxWidth)
			if got != tt.want {
				t.Fatalf("shortenPathWithHome(%q, %q, %d) = %q, want %q", tt.path, tt.home, tt.maxWidth, got, tt.want)
			}
			if TextWidth(got) > tt.maxWidth && tt.maxWidth > 0 {
				t.Fatalf("输出宽度 %d 超过预算 %d: %q", TextWidth(got), tt.maxWidth, got)
			}
		})
	}
}

// TestShortenPathWithHomeCJK 验证 CJK 组件按字素缩写与裁剪，不会被切碎。
func TestShortenPathWithHomeCJK(t *testing.T) {
	path := "/home/用户/项目/内部/模块/tui/渲染.go"
	home := "/home/用户"

	got := shortenPathWithHome(path, home, 100)
	if want := "~/项目/内部/模块/tui/渲染.go"; got != want {
		t.Fatalf("宽度足够时应原样替换主目录，得到 %q，want %q", got, want)
	}

	got = shortenPathWithHome(path, home, 20)
	if want := "~/…/模/tui/渲染.go"; got != want {
		t.Fatalf("CJK 缩写得到 %q，want %q", got, want)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("输出不是合法的 UTF-8: %q", got)
	}
	if TextWidth(got) > 20 {
		t.Fatalf("输出宽度 %d 超过 20: %q", TextWidth(got), got)
	}
}

// TestShortenPathBounded 对任意预算都保证有界且合法：输出宽度不超过
// maxWidth，不切碎 UTF-8 字符，且非空路径在正预算下不返回空串。
func TestShortenPathBounded(t *testing.T) {
	path := "/home/alice/项目/very-long-directory/nested/module/pkg/渲染器.go"
	home := "/home/alice"
	for width := 1; width <= 60; width++ {
		got := shortenPathWithHome(path, home, width)
		if w := TextWidth(got); w > width {
			t.Fatalf("宽度 %d 时输出宽度 %d 超限: %q", width, w, got)
		}
		if !utf8.ValidString(got) {
			t.Fatalf("宽度 %d 时输出不是合法 UTF-8: %q", width, got)
		}
		if got == "" {
			t.Fatalf("宽度 %d 时非空路径返回了空串", width)
		}
	}
}

// TestTailClip 验证尾部裁剪按字素进行，CJK 不会被从中间切开。
func TestTailClip(t *testing.T) {
	if got := tailClip("abcdefgh", 5); got != "…efgh" {
		t.Fatalf("tailClip(abcdefgh, 5) = %q, want …efgh", got)
	}
	if got := tailClip("渲染器.go", 6); got != "…器.go" {
		t.Fatalf("tailClip(渲染器.go, 6) = %q, want …器.go", got)
	}
	if got := tailClip("abc", 10); got != "abc" {
		t.Fatalf("tailClip(abc, 10) = %q, want abc", got)
	}
	if got := tailClip("abcdef", 1); got != "…" {
		t.Fatalf("tailClip(abcdef, 1) = %q, want …", got)
	}
}

// TestFirstGrapheme 验证首字素提取对 ASCII、CJK 与 ZWJ emoji 均安全。
func TestFirstGrapheme(t *testing.T) {
	cases := [][2]string{
		{"", ""},
		{"abc", "a"},
		{"渲染", "渲"},
		{"👨‍👩‍👧abc", "👨‍👩‍👧"},
	}
	for _, c := range cases {
		if got := firstGrapheme(c[0]); got != c[1] {
			t.Fatalf("firstGrapheme(%q) = %q, want %q", c[0], got, c[1])
		}
	}
}

// TestSmartShortenPathReadsHome 验证公开入口确实读取当前用户主目录。
func TestSmartShortenPathReadsHome(t *testing.T) {
	// os.UserHomeDir 在 Unix 下读 HOME，在 Windows 下读 USERPROFILE。
	home := "/home/tester"
	if runtime.GOOS == "windows" {
		home = `C:\Users\tester`
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("home", home) // Plan 9

	path := home + `/projects/demo/internal/tui/render.go`
	got := smartShortenPath(path, 200)
	if !strings.HasPrefix(got, "~") {
		t.Fatalf("smartShortenPath 应使用主目录缩写，得到 %q", got)
	}
	if w := TextWidth(got); w > 200 {
		t.Fatalf("输出宽度 %d 超过 200: %q", w, got)
	}
}
