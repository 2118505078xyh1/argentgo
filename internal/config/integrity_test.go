package config

import (
	"reflect"
	"testing"
)

// integrityCompleteConfig 返回一份所有必需条目都齐全的配置，用作
// 表驱动测试的基线；各用例只需覆盖自己关心的字段。
func integrityCompleteConfig() Config {
	return Config{
		Model:        "gpt-4o-mini",
		BaseURL:      "https://api.openai.com/v1",
		Protocol:     "openai-chat-completions",
		CWD:          "/work",
		StorageDir:   "/work/.argentgo",
		ProviderName: "openai",
		APIKey:       "sk-test",
	}
}

func TestCheckIntegrity(t *testing.T) {
	envWith := func(values map[string]string) func(string) string {
		return func(k string) string { return values[k] }
	}
	noEnv := func(string) string { return "" }

	tests := []struct {
		name string
		cfg  Config
		env  func(string) string
		want []MissingEntry
	}{
		{
			name: "complete config has no missing entries",
			cfg:  integrityCompleteConfig(),
			env:  noEnv,
			want: nil,
		},
		{
			name: "missing required fields",
			cfg: func() Config {
				c := integrityCompleteConfig()
				c.Model = ""
				c.BaseURL = ""
				c.Protocol = ""
				return c
			}(),
			env: noEnv,
			want: []MissingEntry{
				{Entry: "$.model", Reason: "model is required", Fatal: true},
				{Entry: "$.baseURL", Reason: "baseURL is required", Fatal: true},
				{Entry: "$.protocol", Reason: "protocol is required", Fatal: true},
			},
		},
		{
			name: "missing workspace paths",
			cfg: func() Config {
				c := integrityCompleteConfig()
				c.CWD = ""
				c.StorageDir = ""
				return c
			}(),
			env: noEnv,
			want: []MissingEntry{
				{Entry: "$.cwd", Reason: "working directory is required", Fatal: true},
				{Entry: "$.storageDir", Reason: "storage directory is required", Fatal: true},
			},
		},
		{
			name: "missing provider credential is non-fatal",
			cfg: func() Config {
				c := integrityCompleteConfig()
				c.APIKey = ""
				return c
			}(),
			env: noEnv,
			want: []MissingEntry{
				{
					Entry:  "$.providers.openai.apiKey",
					Reason: `provider "openai" has no credential: set apiKey or provide OPENAI_API_KEY at runtime`,
					Fatal:  false,
				},
			},
		},
		{
			name: "credential supplied via injected env is not missing",
			cfg: func() Config {
				c := integrityCompleteConfig()
				c.APIKey = ""
				return c
			}(),
			env:  envWith(map[string]string{"OPENAI_API_KEY": "sk-from-env"}),
			want: nil,
		},
		{
			name: "nil env counts as no env credential",
			cfg: func() Config {
				c := integrityCompleteConfig()
				c.APIKey = ""
				return c
			}(),
			env: nil,
			want: []MissingEntry{
				{
					Entry:  "$.providers.openai.apiKey",
					Reason: `provider "openai" has no credential: set apiKey or provide OPENAI_API_KEY at runtime`,
					Fatal:  false,
				},
			},
		},
		{
			name: "unresolvable provider is fatal",
			cfg: func() Config {
				c := integrityCompleteConfig()
				c.ProviderName = "does-not-exist"
				return c
			}(),
			env: noEnv,
			want: []MissingEntry{
				{Entry: "$.provider", Reason: "no provider is configured or resolvable", Fatal: true},
			},
		},
		{
			name: "local provider needs no credential",
			cfg: func() Config {
				c := integrityCompleteConfig()
				c.ProviderName = "ollama"
				c.APIKey = ""
				return c
			}(),
			env:  noEnv,
			want: nil,
		},
		{
			name: "multiple missing entries aggregate in one pass",
			cfg: func() Config {
				c := integrityCompleteConfig()
				c.Model = ""
				c.CWD = ""
				c.APIKey = ""
				return c
			}(),
			env: noEnv,
			want: []MissingEntry{
				{Entry: "$.model", Reason: "model is required", Fatal: true},
				{Entry: "$.cwd", Reason: "working directory is required", Fatal: true},
				{
					Entry:  "$.providers.openai.apiKey",
					Reason: `provider "openai" has no credential: set apiKey or provide OPENAI_API_KEY at runtime`,
					Fatal:  false,
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.cfg.CheckIntegrity(tc.env)
			if len(got) != len(tc.want) {
				t.Fatalf("CheckIntegrity() = %+v, want %+v", got, tc.want)
			}
			gotByEntry := make(map[string]MissingEntry, len(got))
			for _, entry := range got {
				if _, dup := gotByEntry[entry.Entry]; dup {
					t.Fatalf("duplicate entry %q in %+v", entry.Entry, got)
				}
				gotByEntry[entry.Entry] = entry
			}
			for _, want := range tc.want {
				entry, ok := gotByEntry[want.Entry]
				if !ok {
					t.Errorf("missing entry %q; got %+v", want.Entry, gotByEntry)
					continue
				}
				if !reflect.DeepEqual(entry, want) {
					t.Errorf("entry %q = %+v, want %+v", want.Entry, entry, want)
				}
			}
		})
	}
}
