package tui

import (
	"bytes"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Kind 枚举 UI 理解的逻辑按键。转义序列被
// 解码为这种形状，使模型永远看不到原始字节。
type Kind int

// 按键种类。
const (
	KindRune Kind = iota
	KindEnter
	KindNewline // ctrl+j: insert a line break instead of sending
	KindBackspace
	KindDelete
	KindLeft
	KindRight
	KindUp
	KindDown
	KindHome
	KindEnd
	KindPageUp
	KindPageDown
	KindScrollUp
	KindScrollDown
	KindTab
	KindBackTab
	KindEsc
	KindCtrlC
	KindCtrlA
	KindCtrlE
	KindCtrlK
	KindCtrlU
	KindCtrlW
	KindCtrlL
	KindAltEnter
	KindPaste
	KindIgnore

	// 面板切换被追加在后面，使上面按键的数值
	// 对直接把字节解码为 Kind 值的调用方保持不变。
	KindCtrlT // toggle the tasks panel
	KindCtrlB // toggle the workspace panel
	KindCtrlP // toggle the plan panel
	KindCtrlS // toggle the session rail
	KindAltU  // undo last turn
	KindAltR  // retry last turn

	// 鼠标报告种类被追加在后面，使上面每个按键的数值
	// 保持稳定。由于历史解码（见 parseCSI），滚轮和
	// 未修改的左键在 Key 上保留其历史 Kind 值；
	// Key.Mouse 始终是鼠标报告的权威描述。
	KindMousePress
	KindMouseDrag
	KindMouseRelease
	KindMouseWheelUp
	KindMouseWheelDown

	KindCtrlG // ctrl+g: edit the prompt in $VISUAL / $EDITOR
)

// MouseKind 对一条解码后的 SGR 鼠标报告分类。
type MouseKind int

// 鼠标报告种类。
const (
	MouseUnknown MouseKind = iota
	MousePress
	MouseDrag
	MouseRelease
	MouseWheelUp
	MouseWheelDown
)

// MouseEvent 是一条解码后的 SGR 鼠标报告。Col 和 Row 是基于 1 的
// 终端坐标。Button 保存原始 SGR 位：低两位标识
// 左键 (0)、中键 (1) 和右键 (2)；第 32 位标记拖拽；64/65 是滚轮。
type MouseEvent struct {
	Kind   MouseKind
	Button int
	Col    int
	Row    int
}

// Key 是一次解码后的按键。仅对解码出的鼠标报告 Mouse 才非 nil；
// 键盘按键保持不变。
type Key struct {
	Kind  Kind
	Rune  rune
	Text  string      // pasted block, for KindPaste
	Mouse *MouseEvent // decoded SGR mouse report, if any
}

// Decoder 把字节流转换为按键。不完整的转义序列在
// 两次读取之间保留，因此跨两次读取拆分的序列不会丢失。
type Decoder struct {
	buf []byte
}

// Pending 报告是否有不完整的序列被缓冲。
func (d *Decoder) Pending() bool { return len(d.buf) > 0 }

// Feed 追加字节并返回其中包含的所有完整按键。
func (d *Decoder) Feed(p []byte) []Key {
	if len(p) > 0 {
		d.buf = append(d.buf, p...)
	}
	return d.drain()
}

// FlushEscape 把单个待处理的 ESC 变成 Esc 按键。run 循环在没有
// 新字节迅速到达时调用它，使单个 Esc 仍能立即中止。
func (d *Decoder) FlushEscape() []Key {
	if len(d.buf) != 1 || d.buf[0] != 0x1b {
		return nil
	}
	d.buf = nil
	return []Key{{Kind: KindEsc}}
}

func (d *Decoder) drain() []Key {
	var keys []Key
	for {
		key, used, ok := decodeKey(d.buf)
		if !ok {
			break
		}
		keys = append(keys, key)
		d.buf = d.buf[used:]
	}
	if len(d.buf) == 0 {
		d.buf = nil
	}
	return keys
}

// decodeKey 从 b 的前端解码一个按键。当 b 是更长序列的前缀时，
// 第三个结果为 false。
func decodeKey(b []byte) (Key, int, bool) {
	if len(b) == 0 {
		return Key{}, 0, false
	}
	if b[0] == 0x1b {
		return decodeEscape(b)
	}
	switch c := b[0]; {
	case c == 0x0d:
		return Key{Kind: KindEnter}, 1, true
	case c == 0x0a:
		return Key{Kind: KindNewline}, 1, true
	case c == 0x7f, c == 0x08:
		return Key{Kind: KindBackspace}, 1, true
	case c == 0x09:
		return Key{Kind: KindTab}, 1, true
	case c == 0x03:
		return Key{Kind: KindCtrlC}, 1, true
	case c == 0x01:
		return Key{Kind: KindCtrlA}, 1, true
	case c == 0x05:
		return Key{Kind: KindCtrlE}, 1, true
	case c == 0x0b:
		return Key{Kind: KindCtrlK}, 1, true
	case c == 0x15:
		return Key{Kind: KindCtrlU}, 1, true
	case c == 0x17:
		return Key{Kind: KindCtrlW}, 1, true
	case c == 0x0c:
		return Key{Kind: KindCtrlL}, 1, true
	case c == 0x14:
		return Key{Kind: KindCtrlT}, 1, true
	case c == 0x02:
		return Key{Kind: KindCtrlB}, 1, true
	case c == 0x10:
		return Key{Kind: KindCtrlP}, 1, true
	case c == 0x13:
		return Key{Kind: KindCtrlS}, 1, true
	case c == 0x07:
		return Key{Kind: KindCtrlG}, 1, true
	case c < 0x20:
		return Key{Kind: KindIgnore}, 1, true
	}
	if !utf8.FullRune(b) {
		return Key{}, 0, false
	}
	r, size := utf8.DecodeRune(b)
	if r == utf8.RuneError && size <= 1 {
		return Key{Kind: KindIgnore}, 1, true
	}
	return Key{Kind: KindRune, Rune: r}, size, true
}

// maxPasteBytes 限制括号粘贴缓冲区。如果终端发出
// ESC[200~ 起始符却丢失配对的 ESC[201~，否则会
// 让解码器永远等待，缓冲区不断增长并吞掉每次按键。
// 超过此上限时丢弃开头的 ESC，使累积的字节
// 被正常重新解码，键盘得以恢复。
const maxPasteBytes = 1 << 20 // 1 MiB

func decodeEscape(b []byte) (Key, int, bool) {
	if len(b) == 1 {
		return Key{}, 0, false // a lone ESC waits for FlushEscape
	}
	switch b[1] {
	case '[', 'O':
	case '\r':
		return Key{Kind: KindAltEnter}, 2, true
	case '\n':
		return Key{Kind: KindAltEnter}, 2, true
	case 'u', 'U':
		return Key{Kind: KindAltU}, 2, true
	case 'r', 'R':
		return Key{Kind: KindAltR}, 2, true
	default:
		// ESC 后跟其他内容：Esc 是被单独按下的，
		// 下一个字节是普通按键。
		return Key{Kind: KindEsc}, 1, true
	}
	if b[1] == '[' && len(b) >= 6 && bytes.HasPrefix(b[2:], []byte("200~")) {
		rest := b[6:]
		end := bytes.Index(rest, []byte("\x1b[201~"))
		if end < 0 {
			if len(b) > maxPasteBytes {
				// 结束标记丢失：丢弃 ESC 并把缓冲的
				// 字节作为普通输入重新解码，使键盘永不卡住。
				return Key{Kind: KindIgnore}, 1, true
			}
			return Key{}, 0, false
		}
		return Key{Kind: KindPaste, Text: string(rest[:end])}, 6 + end + 6, true
	}
	for i := 2; i < len(b); i++ {
		if b[i] >= 0x40 && b[i] <= 0x7e {
			return parseCSI(string(b[2:i]), b[i]), i + 1, true
		}
	}
	if len(b) > 32 {
		// 失控的序列：丢弃 ESC 并继续，而不是让
		// 缓冲区无限增长。
		return Key{Kind: KindIgnore}, 1, true
	}
	return Key{}, 0, false
}

// parseCSI 把控制序列的正文和结束字节映射为按键。
func parseCSI(body string, final byte) Key {
	switch final {
	case 'A':
		if body == "1;2" {
			return Key{Kind: KindScrollUp}
		}
		return Key{Kind: KindUp}
	case 'B':
		if body == "1;2" {
			return Key{Kind: KindScrollDown}
		}
		return Key{Kind: KindDown}
	case 'C':
		return Key{Kind: KindRight}
	case 'D':
		return Key{Kind: KindLeft}
	case 'H':
		return Key{Kind: KindHome}
	case 'F':
		return Key{Kind: KindEnd}
	case 'Z':
		return Key{Kind: KindBackTab}
	case '~':
		switch body {
		case "", "1", "7":
			return Key{Kind: KindHome}
		case "3":
			return Key{Kind: KindDelete}
		case "4", "8":
			return Key{Kind: KindEnd}
		case "5":
			return Key{Kind: KindPageUp}
		case "6":
			return Key{Kind: KindPageDown}
		}
		return Key{Kind: KindIgnore}
	case 'M', 'm':
		// SGR 鼠标报告：ESC [ < button ; col ; row M（按下）或 m
		// （释放）。decodeSGRMouse 把完整事件带到 Key.Mouse。
		key, ok := decodeSGRMouse(body, final == 'm')
		if !ok {
			return Key{Kind: KindIgnore}
		}
		// 保留原始滚轮解码器及其基准测试所依赖的历史 Kind 值：
		// 滚轮曾是滚动键，未修改的左键被忽略。run 循环改为按 Key.Mouse 分派，
		// 因此保留这些值不会丢失任何鼠标细节。
		// 因此保留这些值不会丢失任何鼠标细节。
		switch key.Mouse.Kind {
		case MouseWheelUp:
			key.Kind = KindScrollUp
		case MouseWheelDown:
			key.Kind = KindScrollDown
		case MousePress:
			if key.Mouse.Button&0x3 == 0 {
				key.Kind = KindIgnore
			}
		}
		return key
	}
	return Key{Kind: KindIgnore}
}

// decodeSGRMouse 把 SGR 报告的正文（ESC [ 之后的所有内容）
// 解析为带 MouseEvent 的 Key。release 选择结尾的 'm' 形式。第三个
// 结果对任何非格式良好的 SGR 报告为 false，使
// 调用方回退到普通的"忽略"路径。
//
// 旧式 X10 报告（ESC [ M 后跟三个原始字节）有意
// 不解码：SGR 模式（DECSET 1016）下的现代终端只发出
// 这里处理的形式，额外的模式会让字节流解码器复杂化。
func decodeSGRMouse(body string, release bool) (Key, bool) {
	if !strings.HasPrefix(body, "<") {
		return Key{}, false
	}
	parts := strings.Split(strings.TrimPrefix(body, "<"), ";")
	if len(parts) != 3 {
		return Key{}, false
	}
	button, err1 := strconv.Atoi(parts[0])
	col, err2 := strconv.Atoi(parts[1])
	row, err3 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || err3 != nil || button < 0 || col < 1 || row < 1 {
		return Key{}, false
	}
	ev := &MouseEvent{Button: button, Col: col, Row: row}
	key := Key{Mouse: ev}
	switch {
	case button&64 != 0:
		if button&1 != 0 {
			ev.Kind, key.Kind = MouseWheelDown, KindMouseWheelDown
		} else {
			ev.Kind, key.Kind = MouseWheelUp, KindMouseWheelUp
		}
	case release:
		ev.Kind, key.Kind = MouseRelease, KindMouseRelease
	case button&32 != 0:
		ev.Kind, key.Kind = MouseDrag, KindMouseDrag
	default:
		ev.Kind, key.Kind = MousePress, KindMousePress
	}
	return key, true
}

// KeyBinding 是底部按键栏的一项。Key 是首先绘制的标签
// （例如 "ctrl+c"），Desc 是其人可读的含义，Enabled
// 使调用方可以隐藏不适用于当前 UI 状态的条目，而无需
// 重建整个列表。
type KeyBinding struct {
	Key     string
	Desc    string
	Enabled bool
}

const (
	shortHelpSep      = " · "
	shortHelpEllipsis = "…"
)

// HintBindings 把本地化提示字符串（如
// "enter send · alt+enter newline · /help"）拆为绑定，全部启用。它让
// 调用方保留现有区域设置数据，同时通过 ShortHelp 获得逐项控制
// （Enabled、顺序、感知宽度的渲染）。没有
// 描述的按键（"/help"）保留为仅 Key 的绑定。
func HintBindings(hint string) []KeyBinding {
	if strings.TrimSpace(hint) == "" {
		return nil
	}
	parts := strings.Split(hint, "·")
	bindings := make([]KeyBinding, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, desc := part, ""
		if i := strings.IndexFunc(part, unicode.IsSpace); i >= 0 {
			key, desc = part[:i], strings.TrimSpace(part[i:])
		}
		if key == "" {
			continue
		}
		bindings = append(bindings, KeyBinding{Key: key, Desc: desc, Enabled: true})
	}
	return bindings
}

// ShortHelp 按顺序把绑定渲染为 "key desc" 条目，用
// shortHelpSep 连接。禁用的条目被跳过。当连接后的文本
// 放不进 width 时，丢弃整个条目并追加尾随省略号，
// 而不是把某个按键切成两半，因此窄终端绝不会显示
// 被静默截断的绑定。
//
// textWidth 是注入的（通常是 tui.TextWidth），使该辅助函数用
// 调用方的宽度模型测量单元格；nil 回调回退到 TextWidth。
// 当 textWidth 是本包的 TextWidth 时，返回的字符串
// 占用的单元格永不超过 width。
func ShortHelp(bindings []KeyBinding, width int, textWidth func(string) int) string {
	if width <= 0 {
		return ""
	}
	if textWidth == nil {
		textWidth = TextWidth
	}

	segments := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		if !binding.Enabled {
			continue
		}
		var segment string
		switch {
		case binding.Key == "" && binding.Desc == "":
			continue
		case binding.Key == "":
			segment = binding.Desc
		case binding.Desc == "":
			segment = binding.Key
		default:
			segment = binding.Key + " " + binding.Desc
		}
		segments = append(segments, segment)
	}
	if len(segments) == 0 {
		return ""
	}

	full := strings.Join(segments, shortHelpSep)
	if textWidth(full) <= width {
		return full
	}

	// 为省略号预留空间，只保留放得下的完整片段。
	limit := width - textWidth(shortHelpEllipsis)
	if limit < 0 {
		limit = 0
	}
	sepWidth := textWidth(shortHelpSep)
	var out strings.Builder
	used := 0
	for i, segment := range segments {
		w := textWidth(segment)
		if i > 0 {
			w += sepWidth
		}
		if used+w > limit {
			break
		}
		if i > 0 {
			out.WriteString(shortHelpSep)
			used += sepWidth
		}
		out.WriteString(segment)
		used += textWidth(segment)
	}
	return out.String() + shortHelpEllipsis
}

// clipboardWrite 是可插拔的剪贴板后端。测试替换它，
// 以便在不触碰真实系统剪贴板的情况下测试 CopyToClipboard。
var clipboardWrite = writeClipboard

// CopyToClipboard 尽力把文本复制到系统剪贴板。
// 在 Windows 上它通过 os/exec 把文本管道给 clip.exe（仅标准库，无 cgo 且
// 无新模块）。在其他平台，或 clip.exe 不可用时，它是
// 空操作，因此复制支持绝不会破坏 run 循环。存在的
// clip.exe 返回的错误交给调用方记录或忽略。
func CopyToClipboard(text string) error { return clipboardWrite(text) }

func writeClipboard(text string) error {
	if runtime.GOOS != "windows" {
		return nil
	}
	if _, err := exec.LookPath("clip.exe"); err != nil {
		return nil
	}
	cmd := exec.Command("clip.exe")
	cmd.Stdin = strings.NewReader(text)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("tui: copy to clipboard: %w", err)
	}
	return nil
}
