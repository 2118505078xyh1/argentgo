package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/png"

	"github.com/2118505078xyh1/argentgo/internal/computer"
)

// computerToolName 供注册与测试共用。
const computerToolName = "computer"

// computerTool 是桌面级 computer-use 工具：screenshot 看屏幕，
// click/type/key/scroll 按坐标操作。与 browser 互补：Web 用 browser
// 的文本 refs（快），原生桌面应用用 computer（通用但慢）。
type computerTool struct{}

var _ Tool = (*computerTool)(nil)

func (computerTool) Name() string { return computerToolName }
func (computerTool) Description() string {
	return "Control the Windows desktop by screenshot + coordinates: screenshot (see the screen), click(x,y), type(text), key(combo like ctrl+c), scroll(x,y,amount). Use the browser tool for web pages (text refs, faster); use this for native desktop apps. Mutating actions go through approval."
}
func (computerTool) Mutating() bool { return true }
func (computerTool) Schema() string {
	return `{"type":"object","properties":{` +
		`"action":{"type":"string","enum":["screenshot","click","type","key","scroll"],"description":"desktop operation"},` +
		`"x":{"type":"integer","description":"x coordinate for click/scroll"},` +
		`"y":{"type":"integer","description":"y coordinate for click/scroll"},` +
		`"text":{"type":"string","description":"text for type"},` +
		`"key":{"type":"string","description":"key combo for key, e.g. ctrl+c / alt+tab / enter"},` +
		`"amount":{"type":"integer","description":"scroll notches, >0 up / <0 down"}` +
		`},"required":["action"]}`
}

type computerArgs struct {
	Action string `json:"action"`
	X      int    `json:"x"`
	Y      int    `json:"y"`
	Text   string `json:"text"`
	Key    string `json:"key"`
	Amount int    `json:"amount"`
}

func (computerTool) Run(_ context.Context, _ string, raw json.RawMessage) (string, error) {
	var a computerArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("computer: bad arguments: %w", err)
	}
	switch a.Action {
	case "screenshot":
		img, err := computer.Capture()
		if err != nil {
			return "", err
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return "", err
		}
		b64 := base64.StdEncoding.EncodeToString(buf.Bytes())
		return fmt.Sprintf("[desktop screenshot %dx%d, %d bytes png]\n%s",
			img.Bounds().Dx(), img.Bounds().Dy(), buf.Len(), b64), nil

	case "click":
		if err := computer.Click(a.X, a.Y); err != nil {
			return "", err
		}
		return fmt.Sprintf("clicked (%d,%d)", a.X, a.Y), nil

	case "type":
		if a.Text == "" {
			return "", fmt.Errorf("computer: text is required for type")
		}
		if err := computer.Type(a.Text); err != nil {
			return "", err
		}
		return "typed text via clipboard (clipboard contents were replaced)", nil

	case "key":
		if a.Key == "" {
			return "", fmt.Errorf("computer: key is required for key action")
		}
		if err := computer.Key(a.Key); err != nil {
			return "", err
		}
		return fmt.Sprintf("sent key combination %q", a.Key), nil

	case "scroll":
		if a.Amount == 0 {
			return "", fmt.Errorf("computer: amount must be non-zero for scroll")
		}
		if err := computer.Scroll(a.X, a.Y, a.Amount); err != nil {
			return "", err
		}
		return fmt.Sprintf("scrolled %d notches at (%d,%d)", a.Amount, a.X, a.Y), nil
	}
	return "", fmt.Errorf("computer: unknown action %q", a.Action)
}

// RegisterComputer 把 computer 工具显式追加到注册表末尾（幂等）。
// 与 RegisterDebug/RegisterBrowser 一致：默认 New/NewWithLimit 不含它，
// golden 逐字节不变；只有显式调用才加入。
func RegisterComputer(r *Registry) {
	if r == nil {
		return
	}
	tool := computerTool{}
	if _, exists := r.tools[tool.Name()]; exists {
		return
	}
	r.tools[tool.Name()] = tool
	r.order = append(r.order, tool.Name())
}
