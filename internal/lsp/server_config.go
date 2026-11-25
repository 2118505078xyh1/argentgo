package lsp

import (
	"sort"
	"strings"

	"example.com/argentgo/internal/config"
)

// 本文件把 config 解析出的 lsp 块转换成 Registry。它只依赖 config 的中性数据，
// 不读取环境、不派生进程：默认（lsp 块缺失）时产出的注册表与 NewRegistry 完全一致。

// NewRegistryFromConfig 以内置默认服务器为底，叠加配置层的覆盖：
//   - lsp.disabled=true 时返回空注册表（整体关闭）；
//   - 同名条目覆盖内置默认（command/args/env/extensions/initialization）；
//   - disabled 的同名条目从注册表中移除；
//   - 配置中的新名字按稳定顺序追加。
//
// 扩展名会先经 extensionLanguages 归一为 LSP 语言 id（例如 py→python），
// 未收录的扩展名原样作为语言 id，使 LanguageForPath 能命中。
func NewRegistryFromConfig(cfg config.LSPConfig) *Registry {
	registry := &Registry{}
	if cfg.Disabled {
		return registry
	}

	overridden := make(map[string]bool, len(cfg.Servers))
	for name, entry := range cfg.Servers {
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" {
			continue
		}
		// disabled 条目与带 command 的条目都会替换/移除默认服务器。
		if entry.Disabled || len(entry.Command) > 0 || len(entry.Extensions) > 0 {
			overridden[key] = true
		}
	}
	for _, server := range DefaultServers() {
		if overridden[strings.ToLower(server.Name)] {
			continue
		}
		_ = registry.Register(server)
	}

	names := make([]string, 0, len(cfg.Servers))
	for name := range cfg.Servers {
		names = append(names, name)
	}
	sort.Strings(names)

	order := registry.nextOrder
	for _, name := range names {
		entry := cfg.Servers[name]
		if entry.Disabled {
			continue
		}
		server := Server{
			Name:                  strings.TrimSpace(name),
			LanguageIDs:           languageIDsFromExtensions(entry.Extensions),
			Env:                   envFromMap(entry.Env),
			InitializationOptions: entry.Initialization,
		}
		if len(entry.Command) > 0 {
			server.Command = entry.Command[0]
			server.Args = append([]string(nil), entry.Command[1:]...)
		} else {
			// 未给 command 时以服务器名作为可执行程序，方便 "gopls" 这类简写。
			server.Command = server.Name
		}
		_ = registry.Register(server, order)
		order++
	}
	return registry
}

// languageIDsFromExtensions 把配置里的扩展名归一成 LSP 语言 id。
func languageIDsFromExtensions(extensions []string) []string {
	out := make([]string, 0, len(extensions))
	seen := make(map[string]bool, len(extensions))
	for _, ext := range extensions {
		ext = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(ext), "."))
		if ext == "" || seen[ext] {
			continue
		}
		seen[ext] = true
		if language, ok := extensionLanguages[ext]; ok {
			ext = language
		}
		out = append(out, ext)
	}
	return out
}

// envFromMap 把 env 映射编码成有序的 KEY=VALUE 列表，保证逐次运行逐字节一致。
func envFromMap(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+env[key])
	}
	return out
}
