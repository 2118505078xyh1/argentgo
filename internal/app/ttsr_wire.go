package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"example.com/argentgo/internal/agent"
)

// 本文件把 TTSR 规则配置接到 agent.Options。默认（未设置 ARGENT_TTSR_RULES）
// 时返回 nil，agent 侧观察器为 nil，流式路径逐字节不变。

// ttsrRulesEnv 是 TTSR 规则文件的环境变量：值为一个 JSON 文件路径。
// 相对路径按工作区根目录解析。文件不存在/格式错误时降级为不启用（fail-closed）。
const ttsrRulesEnv = "ARGENT_TTSR_RULES"

// loadTTSRRules 读取并编译 TTSR 规则。返回 (rules, err)：
//   - 未设置 env 或值为空：返回 (nil, nil)，表示不启用；
//   - 文件读取失败或 JSON 非法：返回 (nil, err)，调用方应写 warn 并继续（不启用）；
//   - 部分规则正则非法：CompileTTSRRules 跳过它们并在 error 中汇总，合法规则仍返回。
//
// 规则 JSON 形状为 TTSRRule 数组，字段见 agent.TTSRRule。
func loadTTSRRules(env func(string) string, root string) ([]agent.CompiledTTSRRule, error) {
	if env == nil {
		return nil, nil
	}
	value := strings.TrimSpace(env(ttsrRulesEnv))
	if value == "" {
		return nil, nil
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(root, value)
	}
	data, err := os.ReadFile(value)
	if err != nil {
		return nil, err
	}
	var rules []agent.TTSRRule
	if err := json.Unmarshal(data, &rules); err != nil {
		return nil, err
	}
	compiled, cerr := agent.CompileTTSRRules(rules)
	return compiled, cerr
}

// ttsrRulesForRun 是接线用的便捷包装：加载失败时返回 nil 规则（静默降级），
// 使 Execute 主路径不因配置文件问题而失败。非法正则的汇总错误通过 warn 回调上报。
func ttsrRulesForRun(env func(string) string, root string, warn func(string)) []agent.CompiledTTSRRule {
	rules, err := loadTTSRRules(env, root)
	if err == nil {
		return rules
	}
	if warn != nil && err.Error() != "" {
		warn("TTSR 规则加载失败，已禁用：" + err.Error())
	}
	return nil
}
