package provider

import "example.com/argentgo/internal/llm"

// Auth 解析此模板的凭据。调用方拥有请求头名称和值的方案（对 OpenAI 兼容端点是 Authorization/Bearer，
// 对 Anthropic 是 x-api-key/空）以及环境查找，使 config 和 app 能采用该链而无需 provider 表依赖其中任何一个。
//
// NoAuth 胜过每个凭据源。否则 APIKeyEnv 和 APIKeyEnvs 中第一个非空值胜出；
// 没有值时结果是 llm.NoAuth 哨兵且调用方不写请求头。
func (t Template) Auth(lookup func(string) string, header, scheme string) llm.Auth {
	if t.NoAuth {
		return llm.NoAuth()
	}
	names := make([]string, 0, len(t.APIKeyEnvs)+1)
	if t.APIKeyEnv != "" {
		names = append(names, t.APIKeyEnv)
	}
	for _, name := range t.APIKeyEnvs {
		duplicate := false
		for _, existing := range names {
			if existing == name {
				duplicate = true
				break
			}
		}
		if !duplicate {
			names = append(names, name)
		}
	}
	return llm.ResolveAuth(lookup, names, header, scheme)
}
