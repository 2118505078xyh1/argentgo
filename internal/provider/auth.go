package provider

import "github.com/2118505078xyh1/argentgo/internal/llm"

// Auth resolves the credential for this template. The caller owns the header
// name and value scheme (Authorization/Bearer for OpenAI-compatible endpoints,
// x-api-key/empty for Anthropic) and the environment lookup, so config and app
// can adopt the chain without the provider table depending on either.
//
// NoAuth wins over every credential source. Otherwise the first non-empty
// value among APIKeyEnv and APIKeyEnvs wins; with no value the result is the
// llm.NoAuth sentinel and the caller writes no header.
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
