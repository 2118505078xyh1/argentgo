// Package lsp 是最小化的语言服务器协议客户端：带 Content-Length 分帧的 stdio JSON-RPC、编辑后的诊断，以及 definition/hover 查询。
// 带 Content-Length 分帧、编辑后的诊断以及 definition/hover 查询。
//
// 客户端拥有一个子进程且从不猜测语言语义；它
// 启动调用方指定的服务器，保持文档同步，并把
// 普通值交给模型。
package lsp

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// 客户端拥有一个子进程且从不猜测语言语义；它启动调用方指定的服务器，保持文档同步，并把普通值交给模型。
// 分帧限制。超过限制的语言服务器被视为损坏，而非被允许耗尽内存。
const (
	maxHeaderLine  = 8 << 10
	maxMessageSize = 64 << 20
)

// 分帧错误。
var (
	// ErrNoContentLength 报告缺少强制头部的帧。
	ErrNoContentLength = errors.New("lsp: frame has no Content-Length header")
	// ErrMessageTooLarge 报告超过 maxMessageSize 的帧。
	ErrMessageTooLarge = errors.New("lsp: frame is too large")
)

// writeFrame 写入一个分帧的 JSON 正文。
func writeFrame(w io.Writer, payload []byte) error {
	header := "Content-Length: " + strconv.Itoa(len(payload)) + "\r\n\r\n"
	if _, err := io.WriteString(w, header); err != nil {
		return fmt.Errorf("lsp: write header: %w", err)
	}
	if _, err := w.Write(payload); err != nil {
		return fmt.Errorf("lsp: write body: %w", err)
	}
	return nil
}

// readFrame 读取一个分帧的 JSON 正文。流干净结束时返回 io.EOF，帧被截断或畸形时返回错误。
// 流干净结束时返回 io.EOF，帧被截断或畸形时返回错误。
func readFrame(r *bufio.Reader) ([]byte, error) {
	length := -1
	for {
		// ReadSlice 把头部扫描保持在 reader 的固定大小
		// ReadSlice 把头部扫描保持在 reader 的固定大小缓冲区内。ReadString 会无界增长切片寻找 '\n'，因此损坏的服务器可能在下面的大小检查运行之前耗尽内存。
		// 缓冲区内。ReadString 会无界增长切片寻找 '\n'，
		// 因此损坏的服务器可能在下面的大小检查运行之前耗尽内存。
		line, err := r.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) || len(line) > maxHeaderLine {
			return nil, fmt.Errorf("%w: header line over %d bytes", ErrMessageTooLarge, maxHeaderLine)
		}
		if err != nil {
			if errors.Is(err, io.EOF) && len(line) == 0 {
				return nil, io.EOF
			}
			return nil, fmt.Errorf("lsp: read header: %w", err)
		}
		trimmed := strings.TrimRight(string(line), "\r\n")
		if trimmed == "" {
			break
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(key), "content-length") {
			continue
		}
		size, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || size < 0 {
			return nil, fmt.Errorf("lsp: invalid Content-Length %q", value)
		}
		length = size
	}
	if length < 0 {
		return nil, ErrNoContentLength
	}
	if length > maxMessageSize {
		return nil, fmt.Errorf("%w: %d bytes", ErrMessageTooLarge, length)
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, fmt.Errorf("lsp: read body: %w", err)
	}
	return body, nil
}
