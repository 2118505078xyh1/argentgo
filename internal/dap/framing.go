package dap

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// 本文件实现 DAP 的 Content-Length 帧编解码，与 LSP framing 同格式但独立实现
// （零跨包依赖）。所有读都有上界，防止失控 adapter 用巨大长度撑爆内存。

const (
	maxHeaderLine  = 8 << 10
	maxMessageSize = 64 << 20
)

var (
	ErrNoContentLength = errors.New("dap: frame missing Content-Length header")
	ErrMessageTooLarge = errors.New("dap: frame exceeds 64 MiB")
)

// writeFrame 写一帧：Content-Length: N\r\n\r\n<body>。
func writeFrame(w io.Writer, payload []byte) error {
	var b bytes.Buffer
	b.Grow(len(payload) + 64)
	fmt.Fprintf(&b, "Content-Length: %d\r\n\r\n", len(payload))
	b.Write(payload)
	_, err := w.Write(b.Bytes())
	return err
}

// readFrame 从带缓冲的 reader 读一帧。头部大小写不敏感，忽略未知头；
// 正文长度超上限立即拒绝。
func readFrame(r *bufio.Reader) ([]byte, error) {
	contentLength := -1
	for {
		line, err := r.ReadSlice('\n')
		if err != nil {
			return nil, err
		}
		trimmed := strings.TrimRight(string(line), "\r\n")
		if trimmed == "" {
			break // 空行标志头部结束
		}
		if len(line) > maxHeaderLine {
			return nil, fmt.Errorf("dap: header line too long")
		}
		if len(trimmed) >= 15 && strings.EqualFold(trimmed[:15], "Content-Length:") {
			n, err := strconv.Atoi(strings.TrimSpace(trimmed[15:]))
			if err != nil || n < 0 {
				return nil, fmt.Errorf("dap: invalid Content-Length %q", trimmed[15:])
			}
			contentLength = n
		}
	}
	if contentLength < 0 {
		return nil, ErrNoContentLength
	}
	if contentLength > maxMessageSize {
		return nil, ErrMessageTooLarge
	}
	buf := make([]byte, contentLength)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}
