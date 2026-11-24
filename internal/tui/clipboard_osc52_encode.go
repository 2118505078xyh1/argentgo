package tui

import (
	"encoding/base64"
	"fmt"
)

// osc52Sequence 把文本编码成 OSC 52 剪贴板写序列（ESC ] 52 ; c ; <base64> BEL）。
// 跨平台纯函数，便于在无终端环境做单测；实际写入由平台后端负责。
func osc52Sequence(text string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte(text))
	return fmt.Sprintf("\x1b]52;c;%s\x07", encoded)
}
