package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// pngSignature 是 PNG 文件签名，足以让 http.DetectContentType 识别为 image/png。
var pngSignature = []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}

// imageCaptureChatter 记录模型看到的首条用户消息，使 Execute 层的图像注入
// 无需真实模型或网络即可断言。
type imageCaptureChatter struct {
	firstUser llm.Message
	called    bool
}

func (c *imageCaptureChatter) Chat(_ context.Context, _ string, messages []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
	for _, message := range messages {
		if message.Role == "user" && !c.called {
			c.firstUser = message
			c.called = true
			break
		}
	}
	return llm.Message{Role: "assistant", Content: "done"}, nil
}

// TestImageInputEnabledDefaultOn 覆盖 env 门的所有取值：默认开启，
// 仅 ARGENT_IMAGE_INPUT=0（允许首尾空白）时紧急关闭。
func TestImageInputEnabledDefaultOn(t *testing.T) {
	cases := []struct {
		name string
		env  func(string) string
		want bool
	}{
		{name: "nil 读取器默认开启", env: nil, want: true},
		{name: "缺失默认开启", env: envFrom(map[string]string{}), want: true},
		{name: "空值默认开启", env: envFrom(map[string]string{"ARGENT_IMAGE_INPUT": ""}), want: true},
		{name: "0 关闭", env: envFrom(map[string]string{"ARGENT_IMAGE_INPUT": "0"}), want: false},
		{name: "带空白的 0 关闭", env: envFrom(map[string]string{"ARGENT_IMAGE_INPUT": "\t 0 \n"}), want: false},
		{name: "true 保持开启", env: envFrom(map[string]string{"ARGENT_IMAGE_INPUT": "true"}), want: true},
		{name: "01 保持开启", env: envFrom(map[string]string{"ARGENT_IMAGE_INPUT": "01"}), want: true},
		{name: "1 开启", env: envFrom(map[string]string{"ARGENT_IMAGE_INPUT": "1"}), want: true},
		{name: "首尾空格的 1 开启", env: envFrom(map[string]string{"ARGENT_IMAGE_INPUT": " 1 "}), want: true},
		{name: "制表换行的 1 开启", env: envFrom(map[string]string{"ARGENT_IMAGE_INPUT": "\t1\n"}), want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := imageInputEnabled(tc.env); got != tc.want {
				t.Fatalf("imageInputEnabled() = %t, want %t", got, tc.want)
			}
		})
	}
}

// runWithImageInput 在给定工作区用指定的 ARGENT_IMAGE_INPUT 值与提示运行一次
// Execute，返回模型看到的首条用户消息。root 必须与写入图片文件的工作区一致。
func runWithImageInput(t *testing.T, baseEnv func(string) string, root, value, prompt string) llm.Message {
	t.Helper()
	env := func(key string) string {
		if key == "ARGENT_IMAGE_INPUT" {
			return value
		}
		return baseEnv(key)
	}
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: root})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	res.MaxTurns = 1
	res.Approve = autoApprover{}
	chat := &imageCaptureChatter{}
	res.Chatter = chat
	store := testStore(t, res)
	sess, err := store.NewSession("image-input")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(context.Background(), cli.IO{Env: env}, res, sess, prompt, nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !chat.called {
		t.Fatal("chatter 未被调用")
	}
	return chat.firstUser
}

// TestExecuteImageInputWiring 是端到端接线证明：
// 默认开启时 @图片路径 被解析为 ImageInput 并注入首条用户消息的 ContentParts；
// =0 关闭时零调用，首条消息走原纯文本路径。
func TestExecuteImageInputWiring(t *testing.T) {
	baseEnv, root, _ := wiringEnv(t)
	if err := os.WriteFile(filepath.Join(root, "photo.png"), pngSignature, 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("开启时注入图像 part", func(t *testing.T) {
		user := runWithImageInput(t, baseEnv, root, "1", "看这张图 @photo.png")
		if len(user.ContentParts) == 0 || user.ContentParts[0] != '[' {
			t.Fatalf("首条消息未注入 ContentParts: %+v", user)
		}
		if !strings.Contains(string(user.ContentParts), "image/png") {
			t.Fatalf("ContentParts 未包含 image/png: %s", user.ContentParts)
		}
	})

	t.Run("=0 关闭基线逐字节不变", func(t *testing.T) {
		const prompt = "看这张图 @photo.png"
		user := runWithImageInput(t, baseEnv, root, "0", prompt)
		if user.ContentParts != nil {
			t.Fatalf("关闭时 ContentParts 必须为 nil，got %s", user.ContentParts)
		}
		if user.Content != prompt {
			t.Fatalf("关闭时 Content = %q, want %q", user.Content, prompt)
		}
	})
}

// TestExecuteImageInputTolerance 验证路径逃逸、缺图与 CJK 路径的容错：
// 逃逸/缺图产生空 ImageInput 列表，首条消息回落到纯文本；CJK 路径正常注入。
func TestExecuteImageInputTolerance(t *testing.T) {
	baseEnv, root, _ := wiringEnv(t)
	if err := os.WriteFile(filepath.Join(root, "截图.png"), pngSignature, 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("路径逃逸被跳过", func(t *testing.T) {
		const prompt = "看这张图 @../outside.png"
		user := runWithImageInput(t, baseEnv, root, "1", prompt)
		if user.ContentParts != nil {
			t.Fatalf("逃逸路径不应注入图像: %s", user.ContentParts)
		}
		if user.Content != prompt {
			t.Fatalf("回落文本 = %q, want %q", user.Content, prompt)
		}
	})

	t.Run("缺图被跳过", func(t *testing.T) {
		const prompt = "看这张图 @missing.png"
		user := runWithImageInput(t, baseEnv, root, "1", prompt)
		if user.ContentParts != nil {
			t.Fatalf("缺图不应注入图像: %s", user.ContentParts)
		}
		if user.Content != prompt {
			t.Fatalf("回落文本 = %q, want %q", user.Content, prompt)
		}
	})

	t.Run("CJK 路径正常注入", func(t *testing.T) {
		user := runWithImageInput(t, baseEnv, root, "1", "看图 @截图.png")
		if len(user.ContentParts) == 0 {
			t.Fatal("CJK 路径图片未注入")
		}
		if !strings.Contains(string(user.ContentParts), "看") {
			t.Fatalf("CJK 文本未保留: %s", user.ContentParts)
		}
	})
}
