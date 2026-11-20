package main

import (
	"bufio"
	"context"
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
	onEvent func(kind string, raw json.RawMessage)
	// deltaGen is bumped on every onDelta registration. It stamps one
	// streaming round so a superseded round's late result can be dropped.
	// Guarded by mu together with onDelta.
	deltaGen uint64
	closed   bool
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
// workspace is the working directory for the engine. onDelta receives streamed
// prompt chunks; onEvent receives real agent event notifications (kind + the
// raw agent.Event JSON) once SubscribeEvents has registered the subscription.
func startBridge(exePath, workspace string, onDelta func(string), onEvent func(string, json.RawMessage)) (*bridge, error) {
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
		pending: make(map[int]chan rpcReply), onEvent: onEvent,
	}
	if onDelta != nil {
		b.setDeltaHandler(onDelta)
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go b.readLoop()
	return b, nil
}

// setDeltaHandler installs fn as the current prompt/delta callback and returns
// the generation it was installed at. Registration and readLoop's delivery
// snapshot are serialized by mu, so swapping callbacks between streaming rounds
// cannot race with (or tear) a delta being delivered. The returned generation
// lets a caller retire a superseded round before its late result is applied.
func (b *bridge) setDeltaHandler(fn func(string)) uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.deltaGen++
	b.onDelta = fn
	return b.deltaGen
}

// isCurrentDeltaGen reports whether gen still identifies the active streaming
// round. When it returns false a newer round has superseded gen, so any delta
// or completion stamped with gen must be discarded.
func (b *bridge) isCurrentDeltaGen(gen uint64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.deltaGen == gen
}

// readLoop demultiplexes responses, prompt/delta notifications and real
// agent "event" notifications. All callbacks run on this single reader
// goroutine; consumers must marshal UI changes onto their own thread.
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
					if json.Unmarshal(m.Params, &p) == nil {
						// Snapshot the callback under the lock so a concurrent
						// registration on the UI thread cannot race with this
						// read. Call it after unlocking: a handler may itself
						// call back into the bridge (e.g. isCurrentDeltaGen).
						b.mu.Lock()
						fn := b.onDelta
						b.mu.Unlock()
						if fn != nil {
							fn(p.Text)
						}
					}
				case m.Method == "event":
					// event/subscribe forwards each agent event as
					// {"method":"event","params":{"kind":...,"event":...}}.
					// Keep the raw event so the UI can decode only the fields
					// it renders without the bridge importing internal/agent.
					var p struct {
						Kind  string          `json:"kind"`
						Event json.RawMessage `json:"event"`
					}
					if json.Unmarshal(m.Params, &p) == nil && b.onEvent != nil {
						b.onEvent(p.Kind, p.Event)
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

// SubscribeEvents registers the connection with the engine's event hub. The
// call result reports the hub's current seq; from then on every agent event is
// delivered as an "event" notification which readLoop dispatches to onEvent.
//
// ctx bounds the registration call so a stalled engine cannot block startup.
// The subscription itself lives for the lifetime of the RPC transport (EOF or
// bridge close tears it down), so there is no matching unsubscribe method.
func (b *bridge) SubscribeEvents(ctx context.Context) (uint64, error) {
	type answer struct {
		res json.RawMessage
		err error
	}
	out := make(chan answer, 1)
	go func() {
		res, err := b.call("event/subscribe", map[string]any{"after": 0})
		out <- answer{res: res, err: err}
	}()
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case a := <-out:
		if a.err != nil {
			return 0, a.err
		}
		var payload struct {
			Seq uint64 `json:"seq"`
		}
		if err := json.Unmarshal(a.res, &payload); err != nil {
			return 0, err
		}
		return payload.Seq, nil
	}
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
