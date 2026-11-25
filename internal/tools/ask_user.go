package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"example.com/argentgo/internal/pluginapi"
)

// 本文件实现 ask_user 工具：让模型在需要澄清、决策或缺失信息时向用户提出
// 结构化问题，而不是靠猜。工具刻意不进默认注册表（golden 逐字节不变），
// 由 app 在有交互终端时显式调用 RegisterAskUser 注册。

const (
	askUserName           = "ask_user"
	askUserMaxQuestions   = 8
	askUserMaxIDRunes     = 64
	askUserMaxPromptRunes = 500
	askUserMaxOptions     = 12
	askUserMaxOptionRunes = 200
)

type askUserTool struct {
	asker pluginapi.UserAsker
}

func (askUserTool) Name() string { return askUserName }

func (askUserTool) Description() string {
	return "Ask the user structured questions when you need clarification, a decision, " +
		"or information you cannot determine yourself — prefer this over guessing. " +
		"Each question needs a stable id, a prompt, a kind (text/single/multiple), and " +
		"options for single/multiple. Returns answers keyed by id. " +
		"Do NOT use this for tool approvals: mutating tools already have a separate approval flow."
}

func (askUserTool) Mutating() bool { return false }

func (askUserTool) Schema() string {
	return `{"type":"object","properties":{"questions":{"type":"array","description":"1-8 questions to ask the user","items":{"type":"object","properties":{"id":{"type":"string","description":"stable caller-chosen id, echoed back in the answer"},"prompt":{"type":"string","description":"the question shown to the user"},"kind":{"type":"string","enum":["text","single","multiple"],"description":"text=free form, single=pick one option, multiple=pick several"},"options":{"type":"array","description":"choices for single/multiple","items":{"type":"string"}}},"required":["id","prompt","kind"]}}},"required":["questions"]}`
}

func (t askUserTool) Run(ctx context.Context, root string, raw json.RawMessage) (string, error) {
	if t.asker == nil {
		return "", fmt.Errorf("ask_user: no interactive user channel is wired in this session")
	}
	var args struct {
		Questions []pluginapi.UserQuestion `json:"questions"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("ask_user: bad arguments: %w", err)
	}
	questions, err := validateAskUserQuestions(args.Questions)
	if err != nil {
		return "", err
	}
	answers, err := t.asker.AskUser(ctx, questions)
	if err != nil {
		return "", fmt.Errorf("ask_user: %w", err)
	}
	if err := validateAskUserAnswers(questions, answers); err != nil {
		return "", err
	}
	data, err := json.Marshal(answers)
	if err != nil {
		return "", fmt.Errorf("ask_user: encode answers: %w", err)
	}
	return string(data), nil
}

// validateAskUserQuestions 对模型产出问题做有界校验：数量、ID 唯一性、
// prompt/kind/options 的形状与长度，全部是低成本纯检查。
func validateAskUserQuestions(qs []pluginapi.UserQuestion) ([]pluginapi.UserQuestion, error) {
	if len(qs) == 0 {
		return nil, fmt.Errorf("ask_user: questions must be a non-empty array")
	}
	if len(qs) > askUserMaxQuestions {
		return nil, fmt.Errorf("ask_user: too many questions (%d, maximum %d)", len(qs), askUserMaxQuestions)
	}
	out := make([]pluginapi.UserQuestion, 0, len(qs))
	seen := make(map[string]bool, len(qs))
	for i, q := range qs {
		q.ID = strings.TrimSpace(q.ID)
		q.Prompt = strings.TrimSpace(q.Prompt)
		if q.ID == "" {
			return nil, fmt.Errorf("ask_user: question %d has empty id", i+1)
		}
		if utf8.RuneCountInString(q.ID) > askUserMaxIDRunes || strings.ContainsAny(q.ID, "\r\n\x00") {
			return nil, fmt.Errorf("ask_user: question %d id must be at most %d characters with no line breaks", i+1, askUserMaxIDRunes)
		}
		if seen[q.ID] {
			return nil, fmt.Errorf("ask_user: duplicate question id %q", q.ID)
		}
		seen[q.ID] = true
		if q.Prompt == "" {
			return nil, fmt.Errorf("ask_user: question %d has empty prompt", i+1)
		}
		if utf8.RuneCountInString(q.Prompt) > askUserMaxPromptRunes || strings.ContainsAny(q.Prompt, "\x00") {
			return nil, fmt.Errorf("ask_user: question %d prompt must be at most %d characters with no NUL", i+1, askUserMaxPromptRunes)
		}
		switch q.Kind {
		case pluginapi.UserQuestionKindText:
			// text 不需要选项；若模型误传，忽略即可。
			q.Options = nil
		case pluginapi.UserQuestionKindSingle, pluginapi.UserQuestionKindMultiple:
			if len(q.Options) == 0 {
				return nil, fmt.Errorf("ask_user: question %q (%s) needs at least one option", q.ID, q.Kind)
			}
			if len(q.Options) > askUserMaxOptions {
				return nil, fmt.Errorf("ask_user: question %q has too many options (%d, maximum %d)", q.ID, len(q.Options), askUserMaxOptions)
			}
			optSeen := make(map[string]bool, len(q.Options))
			for j, opt := range q.Options {
				opt = strings.TrimSpace(opt)
				if opt == "" || utf8.RuneCountInString(opt) > askUserMaxOptionRunes ||
					strings.ContainsAny(opt, "\r\n\x00") {
					return nil, fmt.Errorf("ask_user: question %q option %d must be a non-empty single-line string of at most %d characters", q.ID, j+1, askUserMaxOptionRunes)
				}
				if optSeen[opt] {
					return nil, fmt.Errorf("ask_user: question %q has duplicate option %q", q.ID, opt)
				}
				optSeen[opt] = true
				q.Options[j] = opt
			}
		default:
			return nil, fmt.Errorf("ask_user: question %q has invalid kind %q (want text/single/multiple)", q.ID, q.Kind)
		}
		out = append(out, q)
	}
	return out, nil
}

// validateAskUserAnswers 保证用户侧实现返回每个问题恰好一条回答，且 ID 对应。
func validateAskUserAnswers(qs []pluginapi.UserQuestion, answers []pluginapi.UserAnswer) error {
	if len(answers) != len(qs) {
		return fmt.Errorf("ask_user: asker returned %d answers for %d questions", len(answers), len(qs))
	}
	byID := make(map[string]bool, len(qs))
	for _, q := range qs {
		byID[q.ID] = true
	}
	got := make(map[string]bool, len(answers))
	for i, a := range answers {
		if !byID[a.ID] {
			return fmt.Errorf("ask_user: answer %d has unknown id %q", i+1, a.ID)
		}
		if got[a.ID] {
			return fmt.Errorf("ask_user: duplicate answer for id %q", a.ID)
		}
		got[a.ID] = true
	}
	return nil
}

// RegisterAskUser 把 ask_user 显式追加到 r 的末尾。幂等；nil 是空操作。
// 它绝不被 New/NewWithLimit 调用，因此默认 Specs()/Names() 与 golden 逐字节不变。
func RegisterAskUser(r *Registry, asker pluginapi.UserAsker) {
	if r == nil {
		return
	}
	if _, exists := r.tools[askUserName]; exists {
		return
	}
	r.tools[askUserName] = askUserTool{asker: asker}
	r.order = append(r.order, askUserName)
}
