package llm_test

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// writePromptImageFile 在 dir 下写入一个图片文件，必要时创建父目录。
func writePromptImageFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}
	return path
}

// pngBytes 返回带合法 PNG 魔数的字节，使 http.DetectContentType 识别为 image/png。
func pngBytes() []byte {
	return []byte("\x89PNG\r\n\x1a\nhello-png")
}

// jpegBytes 返回带合法 JPEG 魔数的字节。
func jpegBytes() []byte {
	return []byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00hello-jpeg")
}

// TestImageInputsFromPromptSingle 单个 @path：返回一条 ImageInput，
// MIME 由文件内容检测得到，Data 与文件字节一致。
func TestImageInputsFromPromptSingle(t *testing.T) {
	root := t.TempDir()
	data := pngBytes()
	writePromptImageFile(t, root, "a.png", data)

	got := llm.ImageInputsFromPrompt("请看 @a.png 这张图", root, 1<<20)
	if len(got) != 1 {
		t.Fatalf("期望 1 张图，得到 %d 张: %+v", len(got), got)
	}
	if got[0].MimeType != "image/png" {
		t.Errorf("MimeType = %q，期望 image/png", got[0].MimeType)
	}
	if !bytes.Equal(got[0].Data, data) {
		t.Errorf("Data = %q，期望 %q", got[0].Data, data)
	}
	if got[0].Detail != "" {
		t.Errorf("Detail = %q，期望空串", got[0].Detail)
	}
}

// TestImageInputsFromPromptMultipleAndOrder 多条引用按出现顺序返回；
// 同一路径出现两次应返回两份，不被去重。
func TestImageInputsFromPromptMultipleAndOrder(t *testing.T) {
	root := t.TempDir()
	pngData := pngBytes()
	jpgData := jpegBytes()
	writePromptImageFile(t, root, "first.png", pngData)
	writePromptImageFile(t, root, "second.jpg", jpgData)

	prompt := "@second.jpg 在前，@first.png 在后，再来一次 @second.jpg"
	got := llm.ImageInputsFromPrompt(prompt, root, 1<<20)
	if len(got) != 3 {
		t.Fatalf("期望 3 张图，得到 %d 张: %+v", len(got), got)
	}
	wantMIME := []string{"image/jpeg", "image/png", "image/jpeg"}
	wantData := [][]byte{jpgData, pngData, jpgData}
	for i := range got {
		if got[i].MimeType != wantMIME[i] {
			t.Errorf("第 %d 张 MimeType = %q，期望 %q", i, got[i].MimeType, wantMIME[i])
		}
		if !bytes.Equal(got[i].Data, wantData[i]) {
			t.Errorf("第 %d 张 Data 不匹配", i)
		}
	}
}

// TestImageInputsFromPromptCJKPath 验证 CJK 文件名与 CJK 文本均可原样处理。
func TestImageInputsFromPromptCJKPath(t *testing.T) {
	root := t.TempDir()
	data := pngBytes()
	writePromptImageFile(t, root, "图片/山色空蒙雨亦奇.png", data)

	prompt := "请看 @图片/山色空蒙雨亦奇.png 🏞"
	got := llm.ImageInputsFromPrompt(prompt, root, 1<<20)
	if len(got) != 1 {
		t.Fatalf("期望 1 张图，得到 %d 张: %+v", len(got), got)
	}
	if got[0].MimeType != "image/png" || !bytes.Equal(got[0].Data, data) {
		t.Errorf("CJK 路径图片解析错误: %+v", got[0])
	}
}

// TestImageInputsFromPromptExtensionCaseInsensitive 扩展名大小写不敏感，
// 且五种允许类型均可识别。
func TestImageInputsFromPromptExtensionCaseInsensitive(t *testing.T) {
	root := t.TempDir()
	data := pngBytes()
	for _, name := range []string{"up.PNG", "mix.JpG", "lower.jpeg", "gif.GIF", "web.WEBP"} {
		writePromptImageFile(t, root, name, data)
	}
	prompt := "@up.PNG @mix.JpG @lower.jpeg @gif.GIF @web.WEBP"
	got := llm.ImageInputsFromPrompt(prompt, root, 1<<20)
	if len(got) != 5 {
		t.Fatalf("期望 5 张图，得到 %d 张: %+v", len(got), got)
	}
	for i := range got {
		if got[i].MimeType != "image/png" {
			t.Errorf("第 %d 张 MimeType = %q，期望 image/png", i, got[i].MimeType)
		}
	}
}

// TestImageInputsFromPromptBadExtensionSkipped 非图片扩展名被跳过。
func TestImageInputsFromPromptBadExtensionSkipped(t *testing.T) {
	root := t.TempDir()
	writePromptImageFile(t, root, "note.txt", []byte("hello"))
	writePromptImageFile(t, root, "noext", []byte("hello"))
	writePromptImageFile(t, root, "ok.png", pngBytes())

	got := llm.ImageInputsFromPrompt("@note.txt @noext @ok.png", root, 1<<20)
	if len(got) != 1 || got[0].MimeType != "image/png" {
		t.Fatalf("期望仅保留 ok.png，得到 %+v", got)
	}
}

// TestImageInputsFromPromptEscapeRejected 相对 root 的 .. 逃逸被拒绝，
// 包括直接逃逸与藏在子目录中的逃逸。
func TestImageInputsFromPromptEscapeRejected(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("创建工作区失败: %v", err)
	}
	writePromptImageFile(t, base, "outside.png", pngBytes())
	writePromptImageFile(t, root, "inside.png", pngBytes())

	prompt := "@../outside.png @sub/../../outside.png @inside.png"
	got := llm.ImageInputsFromPrompt(prompt, root, 1<<20)
	if len(got) != 1 {
		t.Fatalf("期望只剩 inside.png，得到 %d 张: %+v", len(got), got)
	}
	if !bytes.Equal(got[0].Data, pngBytes()) {
		t.Errorf("保留的图片内容不对: %+v", got[0])
	}
}

// TestImageInputsFromPromptAbsoluteRejected 绝对路径被拒绝，即便文件真实存在。
func TestImageInputsFromPromptAbsoluteRejected(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("创建工作区失败: %v", err)
	}
	abs := writePromptImageFile(t, base, "abs.png", pngBytes())

	got := llm.ImageInputsFromPrompt("@"+abs, root, 1<<20)
	if len(got) != 0 {
		t.Fatalf("绝对路径必须被拒绝，得到 %+v", got)
	}
}

// TestImageInputsFromPromptMissingFileSkipped 缺失文件被静默跳过，
// 不阻断后续有效引用。
func TestImageInputsFromPromptMissingFileSkipped(t *testing.T) {
	root := t.TempDir()
	writePromptImageFile(t, root, "ok.png", pngBytes())

	got := llm.ImageInputsFromPrompt("@missing.png @ok.png", root, 1<<20)
	if len(got) != 1 {
		t.Fatalf("期望 1 张图，得到 %d 张: %+v", len(got), got)
	}
	if got[0].MimeType != "image/png" {
		t.Errorf("MimeType = %q，期望 image/png", got[0].MimeType)
	}
}

// TestImageInputsFromPromptMaxBytes 超过 maxBytes 的文件被跳过，
// 恰好等于上限的文件保留。
func TestImageInputsFromPromptMaxBytes(t *testing.T) {
	root := t.TempDir()
	small := pngBytes()
	big := append(pngBytes(), bytes.Repeat([]byte("x"), 64)...)
	writePromptImageFile(t, root, "small.png", small)
	writePromptImageFile(t, root, "big.png", big)

	t.Run("skip oversized", func(t *testing.T) {
		got := llm.ImageInputsFromPrompt("@small.png @big.png", root, int64(len(small)))
		if len(got) != 1 || !bytes.Equal(got[0].Data, small) {
			t.Fatalf("期望只保留 small.png，得到 %+v", got)
		}
	})
	t.Run("exact limit kept", func(t *testing.T) {
		got := llm.ImageInputsFromPrompt("@big.png", root, int64(len(big)))
		if len(got) != 1 || !bytes.Equal(got[0].Data, big) {
			t.Fatalf("恰好等于上限的图片应保留，得到 %+v", got)
		}
	})
	t.Run("negative limit skips all", func(t *testing.T) {
		got := llm.ImageInputsFromPrompt("@small.png", root, -1)
		if len(got) != 0 {
			t.Fatalf("负上限应跳过所有文件，得到 %+v", got)
		}
	})
}

// TestImageInputsFromPromptMIMEDetection MIME 由文件内容而非扩展名检测得到：
// 内容为 JPEG 魔数的文件即便扩展名是 .png，也应得到 image/jpeg。
func TestImageInputsFromPromptMIMEDetection(t *testing.T) {
	root := t.TempDir()
	writePromptImageFile(t, root, "real.png", pngBytes())
	writePromptImageFile(t, root, "jpeg-in-png.png", jpegBytes())
	writePromptImageFile(t, root, "text.png", []byte("plain text"))

	got := llm.ImageInputsFromPrompt("@real.png @jpeg-in-png.png @text.png", root, 1<<20)
	if len(got) != 3 {
		t.Fatalf("期望 3 张图，得到 %d 张: %+v", len(got), got)
	}
	want := []string{"image/png", "image/jpeg", "text/plain; charset=utf-8"}
	for i := range got {
		if got[i].MimeType != want[i] {
			t.Errorf("第 %d 张 MimeType = %q，期望 %q", i, got[i].MimeType, want[i])
		}
	}
}

// TestImageInputsFromPromptEmptyPrompt 空 prompt 与没有任何 @引用时返回空结果。
func TestImageInputsFromPromptEmptyPrompt(t *testing.T) {
	root := t.TempDir()
	writePromptImageFile(t, root, "a.png", pngBytes())

	for _, prompt := range []string{"", "没有图片引用", "@", "@ 后面是空白", "user@example.com"} {
		if got := llm.ImageInputsFromPrompt(prompt, root, 1<<20); len(got) != 0 {
			t.Errorf("prompt %q 期望空结果，得到 %+v", prompt, got)
		}
	}
}

// TestImageInputsFromPromptQuotedPath 引号包围的路径可含空格（可选增强）。
func TestImageInputsFromPromptQuotedPath(t *testing.T) {
	root := t.TempDir()
	name := "带 空格 的图.png"
	data := pngBytes()
	writePromptImageFile(t, root, name, data)

	for _, q := range []string{`"`, "'", "`"} {
		prompt := "请看 @" + q + name + q + " 结束"
		got := llm.ImageInputsFromPrompt(prompt, root, 1<<20)
		if len(got) != 1 || !bytes.Equal(got[0].Data, data) {
			t.Errorf("引号 %q 的路径未被正确解析: %+v", q, got)
		}
	}
}

// TestImageInputsFromPromptDoesNotModifyInput 验证原语不改入参/文件：
// 返回的 Data 是每次调用新读出的切片，修改它不会影响磁盘文件或后续调用。
func TestImageInputsFromPromptDoesNotModifyInput(t *testing.T) {
	root := t.TempDir()
	path := writePromptImageFile(t, root, "a.png", pngBytes())
	prompt := "看 @a.png"
	rootBefore := root
	promptBefore := prompt

	first := llm.ImageInputsFromPrompt(prompt, root, 1<<20)
	if len(first) != 1 {
		t.Fatalf("期望 1 张图，得到 %d 张", len(first))
	}
	first[0].Data[0] = 0x00
	first[0].MimeType = "tampered"

	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("重读文件失败: %v", err)
	}
	if !bytes.Equal(onDisk, pngBytes()) {
		t.Errorf("磁盘文件被修改了: %q", onDisk)
	}
	second := llm.ImageInputsFromPrompt(prompt, root, 1<<20)
	if len(second) != 1 || !bytes.Equal(second[0].Data, pngBytes()) || second[0].MimeType != "image/png" {
		t.Errorf("后续调用受到前次修改影响: %+v", second)
	}
	if root != rootBefore || prompt != promptBefore {
		t.Error("入参字符串被修改")
	}
}

// TestImageInputsFromPromptDeterministic 相同入参与文件状态多次调用结果完全一致。
func TestImageInputsFromPromptDeterministic(t *testing.T) {
	root := t.TempDir()
	writePromptImageFile(t, root, "a.png", pngBytes())
	writePromptImageFile(t, root, "b.jpg", jpegBytes())
	prompt := "@a.png 和 @b.jpg"

	first := llm.ImageInputsFromPrompt(prompt, root, 1<<20)
	for i := 0; i < 5; i++ {
		next := llm.ImageInputsFromPrompt(prompt, root, 1<<20)
		if !reflect.DeepEqual(first, next) {
			t.Fatalf("第 %d 次调用结果不一致: %+v vs %+v", i, first, next)
		}
	}
}
