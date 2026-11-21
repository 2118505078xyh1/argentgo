package server

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// 本文件提供「表面事件编解码」原语：把与传输无关的统一事件帧 SurfaceFrame
// 编码成各表面（SSE / JSON-RPC / ACP）所需的字节形态。
//
// 背景：审计短板——三个表面各自拼接事件帧，SSE 用 "data: ...\n\n"，
// RPC / ACP 用裸 JSON 加换行，格式不统一且难以脱离服务器单独验证。这里先
// 把编码能力落地为一个纯函数：它不读取任何全局状态、不触碰任何现有发送
// 路径、零调用方，因此可在不启动服务器、不改动线协议的前提下验证。
// 接线是可选的；本文件只提供能力，不改变现有 SSE/RPC/ACP 发送路径任何一行。

const (
	// FormatSSE 是 Server-Sent Events 表面：每帧以 "data: " 起始、以两个换行结束。
	FormatSSE = "sse"
	// FormatRPC 是 JSON-RPC 表面：每帧是裸 JSON 加一个换行。
	FormatRPC = "rpc"
	// FormatACP 是 ACP 表面：每帧是裸 JSON 加一个换行。
	FormatACP = "acp"
)

// SurfaceFrame 是与传输无关的统一事件帧。
//   - Kind：事件类型（与 agent.Event.Kind 的字符串形式一致，支持 CJK）；
//   - Seq：单调递增的序列号；
//   - AtMS：事件发生时刻（Unix 毫秒）；
//   - Payload：结构化负载；nil 与空 map 都编码为 {}；
//   - Format：目标表面，取 FormatSSE / FormatRPC / FormatACP。
type SurfaceFrame struct {
	Kind    string
	Seq     uint64
	AtMS    int64
	Payload map[string]any
	Format  string
}

// EncodeFrame 把 f 编码成目标表面所需的字节。
//
// JSON 部分由 map[string]any 编码：encoding/json 会把键按字典序输出，且
// SetEscapeHTML(false) 让 CJK 与 <、>、& 按 UTF-8 原样输出，不产生 \uXXXX
// 转义；Encoder 追加的末尾换行会被去掉，再由信封添加自己的换行。因此相同
// 输入永远得到逐字节一致的切片。
//
// 信封规则（{json} 指上述稳定 JSON，不含末尾换行）：
//   - sse："data: " + {json} + "\n\n"
//   - rpc：{json} + "\n"
//   - acp：{json} + "\n"
//
// JSON 对象固定含 at_ms / format / kind / payload / seq 五个键（按字典序），
// 让每帧自带格式信息、可被独立解读。Payload 为 nil 或空 map 时统一输出 {}。
// 函数只读 f，不修改入参。Format 不是上述三种取值时返回错误，避免把未知
// 表面悄悄编码成默认形态。
func EncodeFrame(f SurfaceFrame) ([]byte, error) {
	// 先确定信封前后缀；未知表面直接报错，避免静默降级。
	var prefix, suffix string
	switch f.Format {
	case FormatSSE:
		prefix, suffix = "data: ", "\n\n"
	case FormatRPC, FormatACP:
		prefix, suffix = "", "\n"
	default:
		return nil, fmt.Errorf("surface_codec: unknown format %q", f.Format)
	}

	// nil / 空 Payload 都归一成空对象，保证「空」形态稳定且不改写入参。
	payload := f.Payload
	if payload == nil {
		payload = map[string]any{}
	}

	// 用 map 编码以获得键的字典序；encoding/json 对 map 键排序，
	// 对嵌套的 Payload map 同样排序。
	body := map[string]any{
		"at_ms":   f.AtMS,
		"format":  f.Format,
		"kind":    f.Kind,
		"payload": payload,
		"seq":     f.Seq,
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(body); err != nil {
		return nil, fmt.Errorf("surface_codec: encode frame: %w", err)
	}
	jsonBytes := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))

	out := make([]byte, 0, len(prefix)+len(jsonBytes)+len(suffix))
	out = append(out, prefix...)
	out = append(out, jsonBytes...)
	out = append(out, suffix...)
	return out, nil
}

// FrameKind 返回帧的事件类型 Kind。纯函数，不做任何归一化或裁剪，
// 因此 CJK 等任意 Unicode 类型名都原样返回。
func FrameKind(f SurfaceFrame) string {
	return f.Kind
}
