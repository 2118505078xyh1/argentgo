package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"example.com/argentgo/internal/agent"
	"example.com/argentgo/internal/llm"
	"example.com/argentgo/internal/tools"
)

// 本文件把 #47 的 switch_model 接到 app 装配层。核心做法是在宿主 Chatter 外包一层
// 可切换模型的小包装：agent 每轮调用 Chatter.Chat 时都传入 model，包装器据此替换为
// 当前生效的 profile 模型，因此不需要改动 agent 循环即可在运行中换模型。
// 切换只影响后续请求，父级配置与会话记录不被就地改写。

// switchableChatter 是支持运行中换模型的 Chatter 包装。它转发所有可选接口，
// 以免破坏字节回放（RawChatter）与流中断回补（ChatOnce）能力。
type switchableChatter struct {
	inner llm.Chatter
	mu    sync.RWMutex
	model string
}

func newSwitchableChatter(inner llm.Chatter) *switchableChatter {
	return &switchableChatter{inner: inner}
}

func (s *switchableChatter) Chat(ctx context.Context, model string, messages []llm.Message, toolSpecs []llm.ToolSpec, onText func(string)) (llm.Message, error) {
	return s.inner.Chat(ctx, s.resolve(model), messages, toolSpecs, onText)
}

func (s *switchableChatter) ChatRaw(ctx context.Context, model string, body []byte, onText func(string)) (llm.Message, error) {
	raw, ok := s.inner.(llm.RawChatter)
	if !ok {
		return llm.Message{}, fmt.Errorf("switch_model: underlying chatter does not support raw replay")
	}
	return raw.ChatRaw(ctx, s.resolve(model), body, onText)
}

func (s *switchableChatter) ChatOnce(ctx context.Context, model string, messages []llm.Message, toolSpecs []llm.ToolSpec) (llm.Message, error) {
	once, ok := s.inner.(interface {
		ChatOnce(context.Context, string, []llm.Message, []llm.ToolSpec) (llm.Message, error)
	})
	if !ok {
		return llm.Message{}, fmt.Errorf("switch_model: underlying chatter does not support non-streaming completion")
	}
	return once.ChatOnce(ctx, s.resolve(model), messages, toolSpecs)
}

func (s *switchableChatter) resolve(model string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if strings.TrimSpace(s.model) != "" {
		return s.model
	}
	return model
}

func (s *switchableChatter) setModel(model string) {
	s.mu.Lock()
	s.model = strings.TrimSpace(model)
	s.mu.Unlock()
}

func (s *switchableChatter) currentModel() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.model
}

// profileModel 从配置里的 profile 定义中读出它声明的模型名。缺失时返回空串。
func profileModel(profile map[string]any) string {
	for _, key := range []string{"model", "modelName", "model_name"} {
		if value, ok := profile[key].(string); ok {
			if value = strings.TrimSpace(value); value != "" {
				return value
			}
		}
	}
	return ""
}

// wireSwitchModel 在所有工具注册表上注册 switch_model，并返回唯一的包装 Chatter。
// 调用方需把返回值装进 agent.Options.Chatter（以及 fallback 链的 primary），否则切换不会生效。
// profiles 来自已配置 profile 的名字集合——它就是白名单的全部内容。
func wireSwitchModel(res Resolved, regs []*tools.Registry, emit func(agent.Event)) llm.Chatter {
	wrapped := newSwitchableChatter(res.Chatter)
	profiles := make([]string, 0, len(res.Cfg.Profiles))
	for name := range res.Cfg.Profiles {
		profiles = append(profiles, name)
	}
	sort.Strings(profiles)

	apply := func(profileName, reason string) (string, error) {
		definition, ok := res.Cfg.Profiles[profileName]
		if !ok {
			return "", fmt.Errorf("profile %q has no definition", profileName)
		}
		model := profileModel(definition)
		if model == "" {
			return "", fmt.Errorf("profile %q does not declare a model", profileName)
		}
		wrapped.setModel(model)
		if emit != nil {
			emit(agent.Event{Kind: agent.EventNotice, ToolName: "switch_model", Text: fmt.Sprintf(
				`{"event":"model_switch","profile":%q,"active_model":%q,"reason":%q}`,
				profileName, model, reason,
			)})
		}
		return model, nil
	}
	for _, reg := range regs {
		tools.RegisterSwitchModel(reg, profiles, apply)
	}
	return wrapped
}
