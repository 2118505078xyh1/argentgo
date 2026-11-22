// Package cdp 提供一个纯标准库的 Chrome DevTools Protocol 客户端。
// 它实现最小可用的 RFC 6455 WebSocket（文本帧），不依赖任何第三方库，
// 符合 argentgo 的 stdlib-only / 无 cgo 约束。
package cdp

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Conn 是一条到 CDP WebSocket 端点的连接。文本消息的读写被互斥锁串行化。
type Conn struct {
	conn net.Conn
	r    *bufio.Reader

	writeMu   sync.Mutex
	closeOnce sync.Once
	closeErr  error
}

// DialWS 建立到 ws://host:port/path 的 WebSocket 连接并完成 Upgrade 握手。
func DialWS(ctx context.Context, rawURL string) (*Conn, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("cdp: bad websocket url: %w", err)
	}
	if u.Scheme != "ws" && u.Scheme != "http" {
		return nil, fmt.Errorf("cdp: unsupported websocket scheme %q", u.Scheme)
	}
	host := u.Host
	if !strings.Contains(host, ":") {
		host = net.JoinHostPort(host, "80")
	}
	d := &net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, fmt.Errorf("cdp: dial %s: %w", host, err)
	}
	path := u.RequestURI()
	if path == "" {
		path = "/"
	}
	keyBuf := make([]byte, 16)
	if _, err := rand.Read(keyBuf); err != nil {
		_ = conn.Close()
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(keyBuf)
	req := "GET " + path + " HTTP/1.1\r\n" +
		"Host: " + host + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n\r\n"
	if _, err := io.WriteString(conn, req); err != nil {
		_ = conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("cdp: websocket handshake: %w", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		_ = conn.Close()
		return nil, fmt.Errorf("cdp: websocket handshake status %d", resp.StatusCode)
	}
	return &Conn{conn: conn, r: br}, nil
}

// WriteText 发送一条文本消息（客户端帧必须 mask）。
func (c *Conn) WriteText(payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	mask := make([]byte, 4)
	if _, err := rand.Read(mask); err != nil {
		return err
	}
	n := len(payload)
	header := make([]byte, 0, 14)
	header = append(header, 0x81) // FIN + text opcode
	switch {
	case n < 126:
		header = append(header, byte(0x80|n))
	case n < 1<<16:
		header = append(header, 0x80|126, byte(n>>8), byte(n))
	default:
		header = append(header, 0x80|127)
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(n))
		header = append(header, b[:]...)
	}
	header = append(header, mask...)
	masked := make([]byte, n)
	for i, b := range payload {
		masked[i] = b ^ mask[i%4]
	}
	if _, err := c.conn.Write(append(header, masked...)); err != nil {
		return err
	}
	return nil
}

// ReadText 读取下一条文本消息。控制帧（ping/pong/close）被妥善处理。
func (c *Conn) ReadText() ([]byte, error) {
	for {
		b1, err := c.r.ReadByte()
		if err != nil {
			return nil, err
		}
		fin := b1&0x80 != 0
		opcode := b1 & 0x0f
		b2, err := c.r.ReadByte()
		if err != nil {
			return nil, err
		}
		masked := b2&0x80 != 0
		length := int64(b2 & 0x7f)
		if length == 126 {
			var b [2]byte
			if _, err := io.ReadFull(c.r, b[:]); err != nil {
				return nil, err
			}
			length = int64(binary.BigEndian.Uint16(b[:]))
		} else if length == 127 {
			var b [8]byte
			if _, err := io.ReadFull(c.r, b[:]); err != nil {
				return nil, err
			}
			length = int64(binary.BigEndian.Uint64(b[:]))
		}
		if length < 0 || length > 64<<20 {
			return nil, errors.New("cdp: websocket frame too large")
		}
		var mask [4]byte
		if masked {
			if _, err := io.ReadFull(c.r, mask[:]); err != nil {
				return nil, err
			}
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(c.r, payload); err != nil {
			return nil, err
		}
		if masked {
			for i := range payload {
				payload[i] ^= mask[i%4]
			}
		}
		switch opcode {
		case 0x1: // text
			if !fin {
				return nil, errors.New("cdp: fragmented text frame unsupported")
			}
			return payload, nil
		case 0x8: // close
			return nil, io.EOF
		case 0x9: // ping → pong
			if err := c.writeControl(0xA, payload); err != nil {
				return nil, err
			}
		case 0xA: // pong
			// ignore
		default:
			// ignore unknown control/data frames
		}
	}
}

func (c *Conn) writeControl(opcode byte, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	frame := append([]byte{0x80 | opcode, byte(len(payload))}, payload...)
	_, err := c.conn.Write(frame)
	return err
}

// Close 关闭底层连接。
func (c *Conn) Close() error {
	c.closeOnce.Do(func() { c.closeErr = c.conn.Close() })
	return c.closeErr
}
