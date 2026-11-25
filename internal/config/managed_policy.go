package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// ManagedConfigEnv 是托管策略文件的路径变量（由 MDM / 企业部署注入）。
// 该文件在配置合并的最高层应用，压制用户与项目配置，且 fail-closed：
// 路径存在但文件损坏时 Load 直接失败（#168 / #202）。
const ManagedConfigEnv = "ARGENT_MANAGED_CONFIG"

// ApplyManagedPolicy 把托管策略覆盖应用到已合并的配置树上。
//
// - policyPath 为空（未设置 env）时不改变 tree，是空操作；
// - 文件不存在时按"未部署托管"处理（空操作）——这允许普通用户零配置；
// - 文件存在但无法读取或不是 JSON 对象时返回错误，fail-closed；
// - 策略值的键与配置文件相同（camelCase），逐键覆盖 tree。
//
// 纯函数：只读文件，不改传入的 policyPath。tree 由调用方持有，会被就地修改。
func ApplyManagedPolicy(tree map[string]any, policyPath string) error {
	path := strings.TrimSpace(policyPath)
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 未部署托管
		}
		return fmt.Errorf("config: managed policy %s: %w", path, err)
	}
	var policy map[string]any
	if err := json.Unmarshal(data, &policy); err != nil {
		return fmt.Errorf("config: managed policy %s is invalid JSON: %w", path, err)
	}
	if policy == nil {
		return fmt.Errorf("config: managed policy %s must be a JSON object", path)
	}
	// 托管层位于所有用户层之上：直接覆盖，不做信任判断。
	// 子对象逐键合并，避免托管一个键就抹掉整棵 providers 树。
	for key, value := range policy {
		if child, ok := value.(map[string]any); ok {
			if existing, ok := tree[key].(map[string]any); ok {
				mergeTree(existing, child)
				continue
			}
		}
		tree[key] = value
	}
	return nil
}
