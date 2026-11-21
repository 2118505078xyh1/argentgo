package server

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

// TestEncodeFrameFormats 覆盖三种表面的信封：SSE 为 "data: {json}\n\n"，
// RPC / ACP 为 "{json}\n"。同时验证 JSON 键按字典序、无末尾多余换行。
func TestEncodeFrameFormats(t *testing.T) {
	for _, tc := range []struct {
		name   string
		format string
		want   string
	}{
		{
			name:   "sse 信封",
			format: FormatSSE,
			want:   "data: {\"at_ms\":1000,\"format\":\"sse\",\"kind\":\"message\",\"payload\":{\"text\":\"hi\"},\"seq\":7}\n\n",
		},
		{
			name:   "rpc 信封",
			format: FormatRPC,
			want:   "{\"at_ms\":1000,\"format\":\"rpc\",\"kind\":\"message\",\"payload\":{\"text\":\"hi\"},\"seq\":7}\n",
		},
		{
			name:   "acp 信封",
			format: FormatACP,
			want:   "{\"at_ms\":1000,\"format\":\"acp\",\"kind\":\"message\",\"payload\":{\"text\":\"hi\"},\"seq\":7}\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := SurfaceFrame{
				Kind:    "message",
				Seq:     7,
				AtMS:    1000,
				Payload: map[string]any{"text": "hi"},
				Format:  tc.format,
			}
			got, err := EncodeFrame(f)
			if err != nil {
				t.Fatalf("EncodeFrame() 返回错误: %v", err)
			}
			if string(got) != tc.want {
				t.Fatalf("EncodeFrame() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestEncodeFrameStable 验证字节稳定性：同一输入多次编码逐字节一致，
// 且 Payload 键的插入顺序不影响输出（encoding/json 对 map 键排序）。
func TestEncodeFrameStable(t *testing.T) {
	// a、b 两个 map 插入顺序不同，但内容相同。
	a := map[string]any{"zeta": 1, "alpha": 2, "mid": 3}
	b := map[string]any{"mid": 3, "zeta": 1, "alpha": 2}

	fa := SurfaceFrame{Kind: "k", Seq: 1, AtMS: 2, Payload: a, Format: FormatRPC}
	fb := SurfaceFrame{Kind: "k", Seq: 1, AtMS: 2, Payload: b, Format: FormatRPC}

	gotA, err := EncodeFrame(fa)
	if err != nil {
		t.Fatalf("EncodeFrame(fa) 返回错误: %v", err)
	}
	gotA2, err := EncodeFrame(fa)
	if err != nil {
		t.Fatalf("EncodeFrame(fa) 第二次调用返回错误: %v", err)
	}
	gotB, err := EncodeFrame(fb)
	if err != nil {
		t.Fatalf("EncodeFrame(fb) 返回错误: %v", err)
	}

	if !bytes.Equal(gotA, gotA2) {
		t.Fatalf("同一输入多次编码应逐字节一致: %q vs %q", gotA, gotA2)
	}
	if !bytes.Equal(gotA, gotB) {
		t.Fatalf("键插入顺序不应影响输出: %q vs %q", gotA, gotB)
	}
	want := "{\"at_ms\":2,\"format\":\"rpc\",\"kind\":\"k\",\"payload\":{\"alpha\":2,\"mid\":3,\"zeta\":1},\"seq\":1}\n"
	if string(gotA) != want {
		t.Fatalf("键未按字典序输出: got %q, want %q", gotA, want)
	}
}

// TestEncodeFrameCJK 验证 CJK 负载与 HTML 敏感字符按 UTF-8 原样输出，
// 不产生 \uXXXX 转义（SetEscapeHTML(false)）。
func TestEncodeFrameCJK(t *testing.T) {
	for _, tc := range []struct {
		name string
		f    SurfaceFrame
		want string
	}{
		{
			name: "中文键与值",
			f: SurfaceFrame{
				Kind:    "消息",
				Seq:     1,
				AtMS:    2,
				Payload: map[string]any{"文本": "你好"},
				Format:  FormatRPC,
			},
			want: "{\"at_ms\":2,\"format\":\"rpc\",\"kind\":\"消息\",\"payload\":{\"文本\":\"你好\"},\"seq\":1}\n",
		},
		{
			name: "HTML 敏感字符不转义",
			f: SurfaceFrame{
				Kind:    "kind",
				Seq:     0,
				AtMS:    0,
				Payload: map[string]any{"html": "<tag>&\"x\""},
				Format:  FormatSSE,
			},
			want: "data: {\"at_ms\":0,\"format\":\"sse\",\"kind\":\"kind\",\"payload\":{\"html\":\"<tag>&\\\"x\\\"\"},\"seq\":0}\n\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := EncodeFrame(tc.f)
			if err != nil {
				t.Fatalf("EncodeFrame() 返回错误: %v", err)
			}
			if string(got) != tc.want {
				t.Fatalf("EncodeFrame() = %q, want %q", got, tc.want)
			}
			if strings.Contains(string(got), `\u`) {
				t.Fatalf("不应产生 \\uXXXX 转义: %q", got)
			}
		})
	}
}

// TestEncodeFrameEmpty 验证零值帧与 nil 负载：payload 输出为 {} 而非 null，
// 各表面信封依旧完整。
func TestEncodeFrameEmpty(t *testing.T) {
	for _, tc := range []struct {
		name   string
		format string
		want   string
	}{
		{"sse 空帧", FormatSSE, "data: {\"at_ms\":0,\"format\":\"sse\",\"kind\":\"\",\"payload\":{},\"seq\":0}\n\n"},
		{"rpc 空帧", FormatRPC, "{\"at_ms\":0,\"format\":\"rpc\",\"kind\":\"\",\"payload\":{},\"seq\":0}\n"},
		{"acp 空帧", FormatACP, "{\"at_ms\":0,\"format\":\"acp\",\"kind\":\"\",\"payload\":{},\"seq\":0}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var f SurfaceFrame
			f.Format = tc.format
			got, err := EncodeFrame(f)
			if err != nil {
				t.Fatalf("EncodeFrame() 返回错误: %v", err)
			}
			if string(got) != tc.want {
				t.Fatalf("EncodeFrame() = %q, want %q", got, tc.want)
			}
			if bytes.Contains(got, []byte("null")) {
				t.Fatalf("nil 负载应编码为 {}，出现 null: %q", got)
			}
		})
	}
}

// TestEncodeFrameErrors 验证错误路径：未知表面与无法序列化的负载都返回错误，
// 而不是静默产出字节。
func TestEncodeFrameErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		f    SurfaceFrame
	}{
		{"未知格式", SurfaceFrame{Kind: "k", Format: "websocket"}},
		{"空格式", SurfaceFrame{Kind: "k", Format: ""}},
		{"不可序列化负载", SurfaceFrame{Kind: "k", Format: FormatRPC, Payload: map[string]any{"ch": make(chan int)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := EncodeFrame(tc.f)
			if err == nil {
				t.Fatalf("EncodeFrame() 应返回错误，得到 %q", got)
			}
			if got != nil {
				t.Fatalf("出错时不应返回字节: %q", got)
			}
		})
	}
}

// TestEncodeFrameDoesNotMutateInput 验证编码只读入参：调用前后 Payload 的
// 键值完全不变（nil 负载归一成空 map 也不写回原帧）。
func TestEncodeFrameDoesNotMutateInput(t *testing.T) {
	payload := map[string]any{"b": "二", "a": "一"}
	before := make(map[string]any, len(payload))
	for k, v := range payload {
		before[k] = v
	}
	f := SurfaceFrame{Kind: "k", Seq: 3, AtMS: 4, Payload: payload, Format: FormatRPC}

	if _, err := EncodeFrame(f); err != nil {
		t.Fatalf("EncodeFrame() 返回错误: %v", err)
	}
	if !reflect.DeepEqual(payload, before) {
		t.Fatalf("EncodeFrame() 修改入参: got %v, want %v", payload, before)
	}

	// nil 负载：编码后原帧的 Payload 仍应是 nil。
	var nf SurfaceFrame
	nf.Format = FormatSSE
	if _, err := EncodeFrame(nf); err != nil {
		t.Fatalf("EncodeFrame(nil payload) 返回错误: %v", err)
	}
	if nf.Payload != nil {
		t.Fatalf("nil 负载被写回: %v", nf.Payload)
	}
}

// TestFrameKind 验证 Kind 提取：普通值、空串与 CJK 类型名都原样返回。
func TestFrameKind(t *testing.T) {
	for _, tc := range []struct {
		name string
		f    SurfaceFrame
		want string
	}{
		{"普通 Kind", SurfaceFrame{Kind: "message.update"}, "message.update"},
		{"空 Kind", SurfaceFrame{}, ""},
		{"CJK Kind", SurfaceFrame{Kind: "消息.更新"}, "消息.更新"},
		{"带其它字段也不影响", SurfaceFrame{Kind: "turn.end", Seq: 9, AtMS: 5, Format: FormatACP}, "turn.end"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := FrameKind(tc.f); got != tc.want {
				t.Fatalf("FrameKind() = %q, want %q", got, tc.want)
			}
		})
	}
}
