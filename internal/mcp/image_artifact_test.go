package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// 本文件覆盖 image 内容块落盘的 opt-in 行为：默认逐字节不变、
// 开启后落盘并增强占位符、无目录/坏数据回退、特殊 mime 安全、
// 文件名唯一，以及辅助函数不改入参。

// imageBlock 构造一个 MCP image 内容块。
func imageBlock(mime, data string) map[string]any {
	return map[string]any{"type": "image", "mimeType": mime, "data": data}
}

// callImageTool 用给定的内容块执行一次 tools/call，返回渲染文本。
func callImageTool(t *testing.T, blocks ...map[string]any) string {
	t.Helper()
	response := map[string]any{"content": blocks}
	ft := &fakeTransport{handlers: map[string]func(json.RawMessage) (any, error){
		"tools/call": func(json.RawMessage) (any, error) { return response, nil },
	}}
	c := newFakeClient("fake", ft, time.Second)
	got, err := c.CallTool(context.Background(), "render", nil)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	return got
}

// clearImageEnv 把与 image 落盘相关的开关恢复到默认（关闭）状态。
func clearImageEnv(t *testing.T) {
	t.Helper()
	t.Setenv(imageArtifactEnv, "")
	t.Setenv(blobArtifactDirEnv, "")
	t.Setenv(contentFilterMinPriorityEnv, "")
}

// imageArtifactPath 从增强占位符中解析出产物路径（占位符可能不是输出首行）。
func imageArtifactPath(t *testing.T, got, mime string) string {
	t.Helper()
	prefix := "[image: " + mime + " → "
	i := strings.Index(got, prefix)
	if i < 0 {
		t.Fatalf("占位符格式不符：got %q，期望包含 %q", got, prefix)
	}
	rest := got[i+len(prefix):]
	end := strings.IndexByte(rest, ']')
	if end < 0 {
		t.Fatalf("占位符未闭合：%q", got)
	}
	return rest[:end]
}

// assertSafeArtifactName 断言文件名不含路径分隔符、控制字符或 ".."。
func assertSafeArtifactName(t *testing.T, base string) {
	t.Helper()
	if !strings.HasPrefix(base, blobArtifactPrefix) {
		t.Fatalf("文件名前缀不符：%q", base)
	}
	if strings.Contains(base, "..") {
		t.Fatalf("文件名含 ..：%q", base)
	}
	for _, r := range base {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`/\<>:"|?*`, r) {
			t.Fatalf("文件名含危险字符 %q：%q", r, base)
		}
	}
}

func TestImageArtifactOffByteIdentical(t *testing.T) {
	clearImageEnv(t)
	dir := t.TempDir()
	t.Setenv(blobArtifactDirEnv, dir) // 即便配置了目录，开关关也应无落盘

	payload := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	data := base64.StdEncoding.EncodeToString(payload)
	got := callImageTool(t,
		map[string]any{"type": "text", "text": "prefix"},
		imageBlock("image/png", data),
	)

	const want = "prefix\n[image: image/png]"
	if got != want {
		t.Fatalf("开关关闭时输出不逐字节等于基线：got %q, want %q", got, want)
	}
	if strings.Contains(got, "→") {
		t.Fatalf("开关关闭时占位符不应含路径箭头：%q", got)
	}
	// 目录不应产生任何文件。
	if entries, err := os.ReadDir(dir); err != nil {
		t.Fatalf("读取产物目录：%v", err)
	} else if len(entries) != 0 {
		t.Fatalf("开关关闭时不应落盘，实际文件数=%d", len(entries))
	}
}

func TestImageArtifactOnSavesAndEnhances(t *testing.T) {
	clearImageEnv(t)
	dir := t.TempDir()
	t.Setenv(imageArtifactEnv, "1")
	t.Setenv(blobArtifactDirEnv, dir)

	payload := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0xff}
	data := base64.StdEncoding.EncodeToString(payload)
	got := callImageTool(t,
		map[string]any{"type": "text", "text": "prefix"},
		imageBlock("image/png", data),
	)

	path := imageArtifactPath(t, got, "image/png")
	if !strings.HasPrefix(got, "prefix\n") {
		t.Fatalf("文本块丢失：%q", got)
	}
	if !pathWithinDir(dir, path) {
		t.Fatalf("产物路径逃逸目录：%q", path)
	}
	if !strings.HasSuffix(path, ".png") {
		t.Fatalf("扩展名应为 .png：%q", path)
	}
	if !strings.HasPrefix(filepath.Base(path), blobArtifactPrefix) {
		t.Fatalf("文件名前缀不符：%q", path)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取产物：%v", err)
	}
	if string(onDisk) != string(payload) {
		t.Fatalf("产物内容不符：got %v, want %v", onDisk, payload)
	}
}

func TestImageArtifactOnWithoutDirKeepsBaseline(t *testing.T) {
	clearImageEnv(t)
	t.Setenv(imageArtifactEnv, "true") // 开关开，但未配置产物目录

	data := base64.StdEncoding.EncodeToString([]byte("image bytes"))
	got := callImageTool(t, imageBlock("image/webp", data))

	const want = "[image: image/webp]"
	if got != want {
		t.Fatalf("无产物目录时应保持基线：got %q, want %q", got, want)
	}
}

func TestImageArtifactOnBadBlobKeepsBaseline(t *testing.T) {
	clearImageEnv(t)
	dir := t.TempDir()
	t.Setenv(imageArtifactEnv, "1")
	t.Setenv(blobArtifactDirEnv, dir)

	got := callImageTool(t, imageBlock("image/png", "!!!not base64!!!"))

	const want = "[image: image/png]"
	if got != want {
		t.Fatalf("坏 blob 应静默回退基线：got %q, want %q", got, want)
	}
	if entries, err := os.ReadDir(dir); err != nil {
		t.Fatalf("读取产物目录：%v", err)
	} else if len(entries) != 0 {
		t.Fatalf("坏 blob 不应落盘，实际文件数=%d", len(entries))
	}
}

func TestImageArtifactSpecialMIMESafe(t *testing.T) {
	clearImageEnv(t)
	dir := t.TempDir()
	t.Setenv(imageArtifactEnv, "1")
	t.Setenv(blobArtifactDirEnv, dir)

	payload := []byte("special mime payload")
	data := base64.StdEncoding.EncodeToString(payload)

	cases := []struct {
		name       string
		mime       string
		wantMimeIn string // 占位符中显示的 mime（保持原样，空值回退 unknown）
		wantExt    string
	}{
		{"CJK mime", "图像/png", "图像/png", ".bin"},
		{"path traversal mime", "image/png/../../evil", "image/png/../../evil", ".bin"},
		{"control chars mime", "image/png\r\n[Injected]\x00evil", "image/png\r\n[Injected]\x00evil", ".bin"},
		{"empty mime", "", "unknown", ".bin"},
		{"with params", "image/png; charset=utf-8", "image/png; charset=utf-8", ".png"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := callImageTool(t, imageBlock(tc.mime, data))
			path := imageArtifactPath(t, got, tc.wantMimeIn)

			if !pathWithinDir(dir, path) {
				t.Fatalf("产物路径逃逸目录：%q", path)
			}
			if !strings.HasSuffix(path, tc.wantExt) {
				t.Fatalf("扩展名不符：got %q, want %q", path, tc.wantExt)
			}
			assertSafeArtifactName(t, filepath.Base(path))
			onDisk, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("读取产物：%v", err)
			}
			if string(onDisk) != string(payload) {
				t.Fatalf("产物内容不符：%q", onDisk)
			}
		})
	}
}

func TestImageArtifactUniqueFilenames(t *testing.T) {
	clearImageEnv(t)
	dir := t.TempDir()
	t.Setenv(imageArtifactEnv, "1")
	t.Setenv(blobArtifactDirEnv, dir)

	data := base64.StdEncoding.EncodeToString([]byte("same bytes"))
	got := callImageTool(t, imageBlock("image/png", data), imageBlock("image/png", data))

	lines := strings.Split(got, "\n")
	if len(lines) != 2 {
		t.Fatalf("应有两行占位符：%q", got)
	}
	p1 := imageArtifactPath(t, lines[0], "image/png")
	p2 := imageArtifactPath(t, lines[1], "image/png")
	if p1 == p2 {
		t.Fatalf("两个块的产物路径不应相同：%q", p1)
	}
	for _, p := range []string{p1, p2} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("产物文件不存在 %q：%v", p, err)
		}
	}
}

func TestImageArtifactWithContentFilter(t *testing.T) {
	clearImageEnv(t)
	dir := t.TempDir()
	t.Setenv(imageArtifactEnv, "1")
	t.Setenv(blobArtifactDirEnv, dir)
	t.Setenv(contentFilterMinPriorityEnv, "50")

	data := base64.StdEncoding.EncodeToString([]byte("priority image"))
	low := imageBlock("image/png", data)
	low["annotations"] = map[string]any{"priority": 0.1} // -> 10，应被过滤
	high := imageBlock("image/gif", data)
	high["annotations"] = map[string]any{"priority": 0.9} // -> 90，应保留

	got := callImageTool(t, low, high)
	if strings.Contains(got, "image/png") {
		t.Fatalf("低优先级块应被过滤：%q", got)
	}
	path := imageArtifactPath(t, got, "image/gif")
	if !pathWithinDir(dir, path) || !strings.HasSuffix(path, ".gif") {
		t.Fatalf("保留块的产物路径异常：%q", path)
	}
}

func TestImageArtifactHelpersDoNotMutateInputs(t *testing.T) {
	clearImageEnv(t)
	dir := t.TempDir()
	t.Setenv(imageArtifactEnv, "1")
	t.Setenv(blobArtifactDirEnv, dir)

	data := base64.StdEncoding.EncodeToString([]byte("no mutation"))
	block := callToolBlock{
		ContentBlock: ContentBlock{Type: "image", MIMEType: "image/png"},
		Data:         data,
	}
	original := block

	// imageBlockPlaceholder 不得改动传入的块。
	_ = imageBlockPlaceholder(block)
	if !reflect.DeepEqual(block, original) {
		t.Fatalf("imageBlockPlaceholder 改动了入参：%+v -> %+v", original, block)
	}

	// filterCallToolBlocks 不得改动入参切片及其元素。
	blocks := []callToolBlock{
		{ContentBlock: ContentBlock{Type: "image", MIMEType: "image/png"}, Data: data},
		{ContentBlock: ContentBlock{Type: "image", MIMEType: "image/gif"}, Data: data},
	}
	snapshot := append([]callToolBlock(nil), blocks...)
	_ = filterCallToolBlocks(blocks, 0)
	for i := range blocks {
		if !reflect.DeepEqual(blocks[i], snapshot[i]) {
			t.Fatalf("filterCallToolBlocks 改动了入参[%d]", i)
		}
	}
}

func TestImageArtifactEnabledParsing(t *testing.T) {
	clearImageEnv(t)
	if imageArtifactEnabled() {
		t.Fatal("未设置时应默认关闭")
	}
	for _, v := range []string{"1", "true", "TRUE", " True "} {
		t.Setenv(imageArtifactEnv, v)
		if !imageArtifactEnabled() {
			t.Fatalf("真值 %q 应开启", v)
		}
	}
	for _, v := range []string{"0", "false", "yes", "garbage", ""} {
		t.Setenv(imageArtifactEnv, v)
		if imageArtifactEnabled() {
			t.Fatalf("假值/非法值 %q 应关闭", v)
		}
	}
}
