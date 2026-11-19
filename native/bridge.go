package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
)

// bridge drives the real argentgo engine over its stdio JSON-RPC transport.
// The native UI stays dependency-free of the engine: it spawns `argent --rpc`
// and speaks the same newline-delimited JSON-RPC that --rpc already exposes.
// This keeps argentgo's core stdlib-only (no MyGo dependency baked in).
type bridge struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader

	mu      sync.Mutex
	nextID  int
	pending map[int]chan rpcReply
	onDelta func(string)
	closed  bool
}

type rpcReply struct {
	result json.RawMessage
	err    string
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// startBridge launches `argent --rpc`. exePath is the path to argent.exe;
// workspace is the working directory for the engine.
func startBridge(exePath, workspace string, onDelta func(string)) (*bridge, error) {
	cmd := exec.Command(exePath, "--rpc")
	cmd.Dir = workspace
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, err
	}
	// stderr is inherited so engine errors are visible during development.
	b := &bridge{
		cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout),
		pending: make(map[int]chan rpcReply), onDelta: onDelta,
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go b.readLoop()
	return b, nil
}

// readLoop demultiplexes responses and prompt/delta notifications.
func (b *bridge) readLoop() {
	for {
		line, err := b.stdout.ReadBytes('\n')
		if len(line) > 0 {
			var m rpcMessage
			if json.Unmarshal(line, &m) == nil {
				switch {
				case m.Method == "prompt/delta":
					var p struct {
						Text string `json:"text"`
					}
					if json.Unmarshal(m.Params, &p) == nil && b.onDelta != nil {
						b.onDelta(p.Text)
					}
				case len(m.ID) > 0:
					var id int
					if json.Unmarshal(m.ID, &id) == nil {
						b.mu.Lock()
						ch := b.pending[id]
						delete(b.pending, id)
						b.mu.Unlock()
						if ch != nil {
							reply := rpcReply{result: m.Result}
							if m.Error != nil {
								reply.err = m.Error.Message
							}
							ch <- reply
						}
					}
				}
			}
		}
		if err != nil {
			b.mu.Lock()
			b.closed = true
			for id, ch := range b.pending {
				ch <- rpcReply{err: "engine connection closed"}
				delete(b.pending, id)
			}
			b.mu.Unlock()
			return
		}
	}
}

// call sends a request and waits for its response.
func (b *bridge) call(method string, params any) (json.RawMessage, error) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, fmt.Errorf("engine bridge closed")
	}
	b.nextID++
	id := b.nextID
	ch := make(chan rpcReply, 1)
	b.pending[id] = ch
	b.mu.Unlock()

	req := rpcMessage{JSONRPC: "2.0", ID: json.RawMessage(fmt.Sprint(id)), Method: method}
	if params != nil {
		raw, _ := json.Marshal(params)
		req.Params = raw
	}
	data, _ := json.Marshal(req)
	b.mu.Lock()
	_, err := b.stdin.Write(append(data, '\n'))
	b.mu.Unlock()
	if err != nil {
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
		return nil, err
	}

	reply := <-ch
	if reply.err != "" {
		return nil, fmt.Errorf("%s", reply.err)
	}
	return reply.result, nil
}

// prompt sends a real prompt; delta chunks arrive through onDelta.
func (b *bridge) prompt(text string) (string, error) {
	result, err := b.call("prompt", map[string]any{"text": text})
	if err != nil {
		return "", err
	}
	var payload struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return "", err
	}
	return payload.Text, nil
}

func (b *bridge) abort() error {
	_, err := b.call("abort", nil)
	return err
}

func (b *bridge) close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	b.mu.Unlock()
	b.stdin.Close()
	_ = b.cmd.Process.Kill()
	return b.cmd.Wait()
}
