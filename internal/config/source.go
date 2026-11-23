package config

import "strings"

// ValueSource 标记某个设置最终由哪一层提供。它让安全敏感的代码
// （例如 app.Resolve 对端点重定向的把关）能够区分"用户自己的设置"
// 与"未受信任的项目文件写下的设置"，而不必重新解析配置文件。
//
// 零值 ValueSourceUnset 表示没有任何层覆盖过该值（编译默认值或内置
// 提供方模板仍可能提供它）。手工构造的 Config 的所有来源都是零值。
type ValueSource string

const (
	ValueSourceUnset    ValueSource = ""
	ValueSourceBuiltin  ValueSource = "builtin"
	ValueSourceGlobal   ValueSource = "global"   // ~/.argentgo/config.json
	ValueSourceProject  ValueSource = "project"  // <cwd>/.argent/config.json | <cwd>/argent.json
	ValueSourceProfile  ValueSource = "profile"  // 无法追溯到具体文件的 profile
	ValueSourceEnv      ValueSource = "env"      // ARGENT_* 环境变量
	ValueSourceOverride ValueSource = "override" // -c / CLI flag 覆盖
)

// SettingSources 记录基 URL 与 API key 的来源层。
type SettingSources struct {
	BaseURL ValueSource
	APIKey  ValueSource
}

// settingPair 是 snapshotValues 从配置树里提取的一对相关值。
// 提取逻辑刻意复用 decode/providersFrom 的同一个 stringField 家族，
// 使"值是否变化"的判定与真正进入 Config 的值完全一致（包括别名优先级）。
type settingPair struct {
	baseURL string
	apiKey  string
}

type valueSnapshot struct {
	baseURL   string
	apiKey    string
	lsp       string                 // lsp 块的规范化快照，用于门控项目级配置
	providers map[string]settingPair // 小写提供方名 -> 覆盖值
	profiles  map[string]settingPair // 小写 profile 名 -> 定义值
}

// snapshotValues 提取当前树里所有被追踪设置的"有效值"。
func snapshotValues(tree map[string]any) valueSnapshot {
	snap := valueSnapshot{
		baseURL:   stringFieldDefault(tree, defaultBaseURL, "baseURL", "baseUrl", "base_url"),
		apiKey:    stringFieldDefault(tree, "", "apiKey", "api_key"),
		lsp:       lspSnapshot(tree),
		providers: make(map[string]settingPair),
		profiles:  make(map[string]settingPair),
	}
	if raw, ok := tree["providers"].(map[string]any); ok {
		for name, value := range raw {
			entry, ok := value.(map[string]any)
			if !ok {
				continue
			}
			snap.providers[strings.ToLower(name)] = settingPair{
				baseURL: stringField(entry, "baseURL", "baseUrl", "base_url"),
				apiKey:  stringField(entry, "apiKey", "api_key", "api-key"),
			}
		}
	}
	if raw, ok := tree["profiles"].(map[string]any); ok {
		for name, value := range raw {
			entry, ok := value.(map[string]any)
			if !ok {
				continue
			}
			snap.profiles[strings.ToLower(name)] = settingPair{
				baseURL: stringField(entry, "baseURL", "baseUrl", "base_url"),
				apiKey:  stringField(entry, "apiKey", "api_key", "api-key"),
			}
		}
	}
	return snap
}

// sourceState 在逐层合并时累积来源。只有值真正发生变化的层才会
// 被记录为来源，因此后一层写入相同的值不会抹掉先前层的出处。
type sourceState struct {
	baseURL   ValueSource
	apiKey    ValueSource
	lsp       ValueSource
	providers map[string]SettingSources
	profiles  map[string]SettingSources
}

func newSourceState() *sourceState {
	return &sourceState{
		providers: make(map[string]SettingSources),
		profiles:  make(map[string]SettingSources),
	}
}

// observe 记录 layer 对从 prev 到 cur 之间发生变化的值的所有权。
func (s *sourceState) observe(prev, cur valueSnapshot, layer ValueSource) {
	if cur.baseURL != prev.baseURL {
		s.baseURL = layer
	}
	if cur.apiKey != prev.apiKey {
		s.apiKey = layer
	}
	if cur.lsp != prev.lsp {
		s.lsp = layer
	}
	for name, pair := range cur.providers {
		old := prev.providers[name]
		entry := s.providers[name]
		if pair.baseURL != old.baseURL {
			entry.BaseURL = layer
		}
		if pair.apiKey != old.apiKey {
			entry.APIKey = layer
		}
		s.providers[name] = entry
	}
	for name, pair := range cur.profiles {
		old := prev.profiles[name]
		entry := s.profiles[name]
		if pair.baseURL != old.baseURL {
			entry.BaseURL = layer
		}
		if pair.apiKey != old.apiKey {
			entry.APIKey = layer
		}
		s.profiles[name] = entry
	}
}

// profileLayer 返回某个 profile 定义的来源层。profile 的覆盖值会被
// 合并到顶层；如果它本身来自项目文件，那么合并后的顶层值也必须
// 被视为项目来源，否则项目可以借 profile 绕过信任检查。
func (s *sourceState) profileLayer(name string) ValueSource {
	sources := s.profiles[strings.ToLower(strings.TrimSpace(name))]
	if sources.BaseURL != ValueSourceUnset {
		return sources.BaseURL
	}
	if sources.APIKey != ValueSourceUnset {
		return sources.APIKey
	}
	return ValueSourceProfile
}

// BaseURLFromProject 报告最终生效的顶层 baseURL 是否由项目级配置文件
// （或其中定义的 profile）写入。
func (c Config) BaseURLFromProject() bool {
	return c.BaseURLSource == ValueSourceProject
}

// LSPFromProject 报告 lsp 块是否由项目级配置文件（或其中定义的 profile）写入。
// 装配层据此在 --trust-project 未开启时忽略项目级 LSP 服务器定义。
func (c Config) LSPFromProject() bool {
	return c.LSPSource == ValueSourceProject
}

// ProviderBaseURLSource 返回指定提供方 baseURL 覆盖层的来源。
// 内置提供方或未知名称返回 ValueSourceUnset。
func (c Config) ProviderBaseURLSource(name string) ValueSource {
	return c.ProviderSources[strings.ToLower(strings.TrimSpace(name))].BaseURL
}

// ProviderAPIKeySource 返回指定提供方 apiKey 覆盖层的来源。
func (c Config) ProviderAPIKeySource(name string) ValueSource {
	return c.ProviderSources[strings.ToLower(strings.TrimSpace(name))].APIKey
}
