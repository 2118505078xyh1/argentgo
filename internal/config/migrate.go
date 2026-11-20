package config

import "sort"

// configVersionKey 是配置文件里记录 schema 版本的键名。目前加载路径
// 尚未写入或读取它；这里先集中定义，供将来 opt-in 接线时使用，
// 避免各处硬编码同一个字面量。
const configVersionKey = "version"

// configSchemaVersion 是当前配置文件 schema 的版本号。migrateConfigTree
// 把较低版本的配置树逐级迁移到这个版本。新增不兼容的 schema 变更时，
// 在此加一，并在 configMigrations 中登记对应的一步。
const configSchemaVersion = 2

// configMigration 描述"从上一版本迁移到目标版本"的一组规则。
// topLevel 处理顶层键；nested 处理指定对象（例如 providers 条目）内部的键。
// 规则全部是"旧键改名"：只按完全匹配的 ASCII 键名操作，因此
// CJK 键与值会原样保留，也不会递归改写不认识的内容。
type configMigration struct {
	topLevel map[string]string
	nested   map[string]map[string]string
}

// configMigrations 是迁移注册表，以目标版本为键。索引 0 代表未迁移的
// 初始状态，没有规则；逐级迁移时读取 target = 当前版本 + 1。
//
//   - 目标版本 1：把顶层历史别名键统一成规范键，使 Raw 中的键与解码
//     首选的键一致（这些别名在 decode 中仍被接受，改名不改变取值）。
//   - 目标版本 2：规范化 providers / models 条目内部的历史别名键。
var configMigrations = map[int]configMigration{
	1: {
		topLevel: map[string]string{
			"modelName":                     "model",
			"baseUrl":                       "baseURL",
			"base_url":                      "baseURL",
			"api_key":                       "apiKey",
			"sandbox_mode":                  "sandboxMode",
			"privacy_mode":                  "privacyMode",
			"max_turns":                     "maxTurns",
			"storage_dir":                   "storageDir",
			"activeProfile":                 "profile",
			"verify_command":                "verifyCommand",
			"verify_commands":               "verifyCommands",
			"verify_timeout_ms":             "verifyTimeoutMS",
			"verify_max_reflections":        "verifyMaxReflections",
			"contextWindowTokens":           "contextWindow",
			"context_window":                "contextWindow",
			"stop_token_ids":                "stopTokenIds",
			"auto_condense_context_percent": "autoCondenseContextPercent",
		},
	},
	2: {
		nested: map[string]map[string]string{
			"providers": {
				"api_key":  "apiKey",
				"base_url": "baseURL",
			},
			"models": {
				"contextWindowTokens": "contextWindow",
				"context_window":      "contextWindow",
			},
		},
	},
}

// migrateConfigTree 把 tree 按版本逐级迁移到当前 schema 版本。
//
// 返回值依次为：迁移后的新树、迁移后所处的版本、内容是否发生变化。
// 它不修改入参：先深拷贝整棵树，再在副本上应用改名规则。对 nil 入参
// 返回非 nil 的空树，方便调用方直接使用。
//
// version 小于零时按零处理；大于当前版本时原样返回且报告无变化，
// 以免把未来版本的配置降级。changed 只反映键改名的结果：把版本
// 从 0 推进到当前版本本身不算"内容变化"，因此已经是规范键的旧版
// 配置会得到 changed=false。
//
// 幂等性：对迁移结果再次以返回的新版本调用，不会再次改写内容。
func migrateConfigTree(tree map[string]any, version int) (map[string]any, int, bool) {
	migrated := cloneTree(tree)

	current := version
	if current < 0 {
		current = 0
	}
	changed := false

	for target := current + 1; target <= configSchemaVersion; target++ {
		step, ok := configMigrations[target]
		if !ok {
			// 注册表缺步时保守停下，不跳过可能依赖该步的后续规则。
			break
		}
		if applyConfigMigration(migrated, step) {
			changed = true
		}
		current = target
	}

	return migrated, current, changed
}

// applyConfigMigration 把一步规则应用到已经克隆过的树上。
// nested 规则作用于命名容器（如 providers）里的每个对象条目，
// 而不是容器本身；容器自身或数组中的对象不会被改写。
func applyConfigMigration(tree map[string]any, step configMigration) bool {
	changed := renameConfigKeys(tree, step.topLevel)
	for objectKey, renames := range step.nested {
		container, ok := tree[objectKey].(map[string]any)
		if !ok {
			continue
		}
		for _, entry := range container {
			object, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			if renameConfigKeys(object, renames) {
				changed = true
			}
		}
	}
	return changed
}

// renameConfigKeys 在单个对象内应用"旧键 -> 新键"改名。
// 若新旧键同时存在，保留规范键的值并删除旧键，与 decode 中
// 规范键优先的行为一致；只有确实删掉了旧键才算发生变化。
func renameConfigKeys(object map[string]any, renames map[string]string) bool {
	type pendingRename struct {
		from  string
		to    string
		value any
	}

	pending := make([]pendingRename, 0, len(renames))
	for from, to := range renames {
		if value, exists := object[from]; exists {
			pending = append(pending, pendingRename{from: from, to: to, value: value})
		}
	}
	if len(pending) == 0 {
		return false
	}

	// 先按旧键排序，保证多个别名指向同一规范键时结果确定；
	// 再统一删除所有旧键、写入新键，避免链式改名互相覆盖。
	sort.Slice(pending, func(i, j int) bool { return pending[i].from < pending[j].from })
	for _, rename := range pending {
		delete(object, rename.from)
	}
	for _, rename := range pending {
		if _, exists := object[rename.to]; !exists {
			object[rename.to] = rename.value
		}
	}
	return true
}
