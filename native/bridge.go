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

// bridge 通过其 stdio JSON-RPC 传输驱动真实的 argentgo engine。
// 原生 UI 保持对 engine 无依赖：它启动 `argentgo --rpc`
// 并讲 --rpc 已暴露的同样的换行分隔 JSON-RPC。
// 这使 argentgo 核心保持只用标准库（没有内嵌 MyGo 依赖）。
type bridge struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader

	mu      sync.Mutex
	nextID  int
	pending map[int]chan rpcReply
	onDelta func(string)
	onEvent func(kind string, raw json.RawMessage)
	// onClose 在 readLoop 遇到 EOF/读错误（即 engine 连接断开）时调用，恰好一次。
	// closeGen 在每次注册时增加；onClose 与 closeGen 一起由 mu 保护，
	// 复用 onDelta 的锁下快照模式，避免注册与断开通知相互竞争。
	onClose  func(error)
	closeGen uint64
	// deltaGen 在每次 onDelta 注册时增加。它标记一个
	// 流式轮次，使被取代轮次的迟到结果能被丢弃。
	// 与 onDelta 一起由 mu 保护。
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

// startBridge 启动 `argentgo --rpc`。exePath 是 argentgo.exe 的路径；workspace 是 engine 的工作目录。onDelta 接收流式提示分块；一旦 SubscribeEvents 注册了订阅，onEvent 接收真实 agent 事件通知（kind + 原始 agent.Event JSON）。stderr 被继承，使开发期间 engine 错误可见。
// workspace 是 engine 的工作目录。onDelta 接收流式提示
// 分块；一旦 SubscribeEvents 注册了订阅，onEvent 接收真实 agent 事件
// 通知（kind + 原始 agent.Event JSON）。
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
	// stderr 被继承，使开发期间 engine 错误可见。
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

// setDeltaHandler 安装 fn 为当前提示/delta 回调并返回
// 它被安装时的 generation。注册与 readLoop 的传递
// 快照由 mu 串行化，因此在流式轮次之间交换回调
// 不会与正在传递的 delta 竞争（或撕裂它）。返回的 generation
// 使调用方能在被取代轮次的迟到结果被应用前将其退役。
func (b *bridge) setDeltaHandler(fn func(string)) uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.deltaGen++
	b.onDelta = fn
	return b.deltaGen
}

// isCurrentDeltaGen 报告 gen 是否仍标识活动流式
// 标记 gen 的 delta 或完成都必须丢弃。
func (b *bridge) isCurrentDeltaGen(gen uint64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.deltaGen == gen
}

// setCloseHandler 安装 fn 为连接断开回调，返回安装时的 generation。
// 与 setDeltaHandler 一样，注册在 mu 下完成，readLoop 在锁下快照、
// 解锁后调用，从而在注册与断开通知之间不产生数据竞争或撕裂。
func (b *bridge) setCloseHandler(fn func(error)) uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closeGen++
	b.onClose = fn
	return b.closeGen
}

// readLoop 解复用响应、提示/delta 通知和真实
// agent "event" 通知。所有回调都在这唯一的 reader
// goroutine 上运行；消费者必须把 UI 变更编组到自己的线程。
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
						// 在锁下快照回调，使 UI 线程上的并发
						// 注册不会与这次读取竞争。解锁后
						// 调用它：处理器本身可能回调 bridge
						// （如 isCurrentDeltaGen）。
						b.mu.Lock()
						fn := b.onDelta
						b.mu.Unlock()
						if fn != nil {
							fn(p.Text)
						}
					}
				case m.Method == "event":
					// event/subscribe 把每个 agent 事件转发为
					// 保留原始事件，使 UI 能只解码它渲染的
					// 字段而无需 bridge 导入 internal/agent。
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
			fn := b.onClose
			b.mu.Unlock()
			// 解锁后调用：处理器可能回调 bridge 或编组 UI 变更。
			// readLoop 随即返回，因此每个 bridge 恰好通知一次断开。
			if fn != nil {
				fn(err)
			}
			return
		}
	}
}

// call 发送请求并等待其响应。
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

// SubscribeEvents 把连接注册到 engine 的事件中心。
// 作为 "event" 通知传递，由 readLoop 分发给 onEvent。
//
// ctx 限制注册调用，使卡住的 engine 不会阻塞启动。
// 订阅本身存活于 RPC 传输的整个生命周期（EOF 或
// bridge close 会拆除它），因此没有对应的取消订阅方法。
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

// prompt 发送真实提示；delta 分块通过 onDelta 到达。
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
