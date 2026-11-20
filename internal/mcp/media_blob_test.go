package mcp

import (
	"strings"
	"testing"
)

func TestMediaBlobExtCommonMIME(t *testing.T) {
	tests := []struct {
		name string
		mime string
		want string
	}{
		{"png", "image/png", ".png"},
		{"jpeg", "image/jpeg", ".jpg"},
		{"jpg alias", "image/jpg", ".jpg"},
		{"gif", "image/gif", ".gif"},
		{"webp", "image/webp", ".webp"},
		{"svg", "image/svg+xml", ".svg"},
		{"bmp", "image/bmp", ".bmp"},
		{"wav", "audio/wav", ".wav"},
		{"x-wav", "audio/x-wav", ".wav"},
		{"wave", "audio/wave", ".wav"},
		{"mpeg", "audio/mpeg", ".mp3"},
		{"mp3", "audio/mp3", ".mp3"},
		{"ogg", "audio/ogg", ".ogg"},
		{"flac", "audio/flac", ".flac"},
		{"aac", "audio/aac", ".aac"},
		{"m4a", "audio/x-m4a", ".m4a"},
		{"mp4 video", "video/mp4", ".mp4"},
		{"webm", "video/webm", ".webm"},
		{"mov", "video/quicktime", ".mov"},
		{"pdf", "application/pdf", ".pdf"},
		{"json", "application/json", ".json"},
		{"plain text", "text/plain", ".txt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mediaBlobExt(tt.mime); got != tt.want {
				t.Fatalf("mediaBlobExt(%q) = %q, want %q", tt.mime, got, tt.want)
			}
		})
	}
}

func TestMediaBlobExtUnknownAndEmpty(t *testing.T) {
	unknown := []string{
		"",
		"   ",
		"application/octet-stream",
		"foo/bar",
		"image/x-unknown-format",
		"video/x-matroska",
	}
	for _, mime := range unknown {
		if got := mediaBlobExt(mime); got != ".bin" {
			t.Errorf("mediaBlobExt(%q) = %q, want .bin", mime, got)
		}
	}
}

func TestMediaBlobExtMaliciousMIME(t *testing.T) {
	// 恶意 MIME 必须回退到 .bin，绝不能把路径分隔符、控制字符
	// 或 ".." 带进扩展名。
	malicious := []string{
		"../../etc/passwd",
		`..\..\evil`,
		"image/png/../../evil",
		"image/png\x00/../../evil",
		"image/png\r\nContent-Type: evil",
		"/absolute/path",
		"a/b/c",
		"./../x",
		"image/png\t\x01\x02",
	}
	for _, mime := range malicious {
		if got := mediaBlobExt(mime); got != ".bin" {
			t.Errorf("mediaBlobExt(%q) = %q, want .bin", mime, got)
		}
	}
}

func TestMediaBlobExtCaseInsensitive(t *testing.T) {
	tests := []struct {
		mime string
		want string
	}{
		{"IMAGE/PNG", ".png"},
		{"Image/Jpeg", ".jpg"},
		{"Audio/WAV", ".wav"},
		{"AUDIO/MPEG", ".mp3"},
		{"Application/PDF", ".pdf"},
		{"IMAGE/SVG+XML", ".svg"},
	}
	for _, tt := range tests {
		if got := mediaBlobExt(tt.mime); got != tt.want {
			t.Errorf("mediaBlobExt(%q) = %q, want %q", tt.mime, got, tt.want)
		}
	}
}

func TestMediaBlobExtCJK(t *testing.T) {
	// 含 CJK 的畸形 MIME 无法命中白名单，安全回退到 .bin。
	// 关键是返回值中绝不混入非 ASCII 字节。
	cjk := []string{
		"图像/png",
		"音频/wav",
		"音频/wav；编码=utf-8",
		"图片/jpeg",
		"文档/pdf；名称=测试",
		"视频/mp4文件",
		"IMAGE/图片",
	}
	for _, mime := range cjk {
		got := mediaBlobExt(mime)
		if got != ".bin" {
			t.Errorf("mediaBlobExt(%q) = %q, want .bin", mime, got)
		}
		for _, r := range got {
			if r > 0x7f {
				t.Errorf("mediaBlobExt(%q) returned non-ASCII extension %q", mime, got)
			}
		}
	}
}

func TestMediaBlobExtParametersAndWhitespace(t *testing.T) {
	tests := []struct {
		mime string
		want string
	}{
		{"image/png; charset=utf-8", ".png"},
		{"audio/wav; rate=44100; channels=2", ".wav"},
		{"  application/pdf  ", ".pdf"},
		{"\timage/webp\n", ".webp"},
		{"application/pdf;", ".pdf"},
		{";image/png", ".bin"}, // 分号前为空，不匹配
	}
	for _, tt := range tests {
		if got := mediaBlobExt(tt.mime); got != tt.want {
			t.Errorf("mediaBlobExt(%q) = %q, want %q", tt.mime, got, tt.want)
		}
	}
}

func TestMediaBlobExtAlwaysSafeWhitelist(t *testing.T) {
	// 对所有输入（含各种攻击载荷）返回值都必须来自安全白名单：
	// 以点开头、仅含 ASCII 字母数字、不含路径分隔符或控制字符。
	inputs := []string{
		"image/png", "audio/wav", "application/pdf", "video/mp4",
		"../../evil", `..\..\evil`, "image/png\x00../x", "图像/png",
		"", "   ", "a/b", "\x00\x01\x02", strings.Repeat("a", 500),
	}
	allowed := map[string]bool{
		".png": true, ".jpg": true, ".gif": true, ".webp": true,
		".svg": true, ".bmp": true, ".tiff": true, ".wav": true,
		".mp3": true, ".ogg": true, ".flac": true, ".aac": true,
		".m4a": true, ".mp4": true, ".webm": true, ".mov": true,
		".pdf": true, ".json": true, ".txt": true, ".bin": true,
	}
	for _, mime := range inputs {
		got := mediaBlobExt(mime)
		if !allowed[got] {
			t.Errorf("mediaBlobExt(%q) = %q not in whitelist", mime, got)
		}
		if !strings.HasPrefix(got, ".") || len(got) < 2 {
			t.Errorf("mediaBlobExt(%q) = %q has no safe leading dot", mime, got)
			continue
		}
		for _, r := range got[1:] {
			if !('0' <= r && r <= '9' || 'a' <= r && r <= 'z') {
				t.Errorf("mediaBlobExt(%q) = %q contains unsafe byte %q", mime, got, r)
			}
		}
	}
}

func TestResolveBlobExtOptIn(t *testing.T) {
	// 默认关闭：使用既有映射。audio/flac 不在既有映射中，回退 .bin。
	t.Setenv(mediaBlobExtEnv, "")
	if got := resolveBlobExt("audio/flac"); got != ".bin" {
		t.Fatalf("default resolveBlobExt(audio/flac) = %q, want .bin", got)
	}
	// 显式假值也保持关闭。
	t.Setenv(mediaBlobExtEnv, "false")
	if got := resolveBlobExt("audio/flac"); got != ".bin" {
		t.Fatalf("false resolveBlobExt(audio/flac) = %q, want .bin", got)
	}
	// 选择加入：使用扩展映射，覆盖更多媒体类型。
	t.Setenv(mediaBlobExtEnv, "1")
	if got := resolveBlobExt("audio/flac"); got != ".flac" {
		t.Fatalf("opt-in resolveBlobExt(audio/flac) = %q, want .flac", got)
	}
	// 对既有映射已覆盖的类型，两种映射结果一致（纯增量、无回归）。
	if got := resolveBlobExt("application/pdf"); got != ".pdf" {
		t.Fatalf("opt-in resolveBlobExt(application/pdf) = %q, want .pdf", got)
	}
}
