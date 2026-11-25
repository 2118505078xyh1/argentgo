package app

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"example.com/argentgo/internal/cli"
	"example.com/argentgo/internal/pluginapi"
	"example.com/argentgo/internal/tools"
)

// 本文件把 ask_user 工具接到 app 装配面。第一步只覆盖交互式终端：
// 用 stdin/stdout 做最基础的文本提问，让能力先"通起来"。--rpc / native /
// headless 不注册该工具，避免偷读传输字节（与 PromptApprover 的约束一致）；
// 后续可在 RPC 侧实现结构化表单再替换注入的 asker。

// textUserAsker 是 stdin/stdout 上的最小 UserAsker 实现。
type textUserAsker struct {
	in  io.Reader
	out io.Writer
}

func (a *textUserAsker) AskUser(ctx context.Context, questions []pluginapi.UserQuestion) ([]pluginapi.UserAnswer, error) {
	if a == nil || a.in == nil || a.out == nil {
		return nil, fmt.Errorf("no interactive input/output channel")
	}
	answers := make([]pluginapi.UserAnswer, 0, len(questions))
	for _, q := range questions {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		fmt.Fprintf(a.out, "\n[ask_user] %s\n", q.Prompt)
		switch q.Kind {
		case pluginapi.UserQuestionKindText:
		case pluginapi.UserQuestionKindSingle, pluginapi.UserQuestionKindMultiple:
			for i, opt := range q.Options {
				fmt.Fprintf(a.out, "  %d) %s\n", i+1, opt)
			}
		}
		fmt.Fprint(a.out, "> ")

		line, err := readAskLine(a.in)
		if err != nil {
			if err == io.EOF && len(strings.TrimSpace(line)) == 0 {
				return nil, fmt.Errorf("user closed the input while answering %q", q.ID)
			}
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		value := strings.TrimSpace(line)
		answer := pluginapi.UserAnswer{ID: q.ID}
		switch q.Kind {
		case pluginapi.UserQuestionKindText:
			answer.Value = value
		case pluginapi.UserQuestionKindSingle:
			answer.Value = resolveAskOption(q.Options, value)
		case pluginapi.UserQuestionKindMultiple:
			for _, part := range strings.Split(value, ",") {
				pick := resolveAskOption(q.Options, strings.TrimSpace(part))
				if pick != "" {
					answer.Values = append(answer.Values, pick)
				}
			}
		}
		answers = append(answers, answer)
	}
	return answers, nil
}

// resolveAskOption 把用户输入的序号（1-based）解析为选项文本；
// 若用户直接输入选项文本且能匹配，也原样接受。无法解析时原样返回，
// 让模型自行判断（不替用户做决定）。
func resolveAskOption(options []string, input string) string {
	if input == "" {
		return ""
	}
	if idx, err := strconv.Atoi(input); err == nil && idx >= 1 && idx <= len(options) {
		return options[idx-1]
	}
	for _, opt := range options {
		if opt == input {
			return opt
		}
	}
	return input
}

// readAskLine 从 r 逐字节读一行，不预读额外字节。
// 这很重要：stdio.In 可能与审批器共享，bufio.Reader 的整块预读会吞掉
// 后续审批需要的输入行；逐字节读不会偷读换行符之后的任何字节。
func readAskLine(r io.Reader) (string, error) {
	var b []byte
	one := make([]byte, 1)
	for {
		n, err := r.Read(one)
		if n > 0 {
			switch one[0] {
			case '\n':
				return strings.TrimSuffix(string(b), "\r"), nil
			case 0: // 忽略 NUL
			default:
				b = append(b, one[0])
			}
			continue
		}
		if err != nil {
			if err == io.EOF && len(b) > 0 {
				return string(b), nil
			}
			return "", err
		}
	}
}

// wireAskUser 只在交互式终端为本次运行的全部工具注册表安装 ask_user。
// 非 TTY（--rpc/headless/测试桩）时零副作用：不注册工具、不读 stdin，
// 工具列表与接线之前逐字节不变。
func wireAskUser(stdio cli.IO, set *toolset) {
	if stdio.In == nil || stdio.Out == nil || stdio.HasTerminal == nil || !stdio.HasTerminal() {
		return
	}
	if set == nil || set.base == nil {
		return
	}
	asker := &textUserAsker{in: stdio.In, out: stdio.Out}
	for _, reg := range set.base.regs {
		tools.RegisterAskUser(reg, asker)
	}
}
