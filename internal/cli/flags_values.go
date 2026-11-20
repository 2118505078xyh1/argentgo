package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Overrides 保存重复的 `-c key=value` / `--config key=value` 设置。
// 点分键会构建嵌套对象，例如 `-c tools.include='["read"]'`。
type Overrides struct {
	values map[string]string
}

// Set 解析一条 key=value 覆盖值。它实现 flag.Value。
func (o *Overrides) Set(spec string) error {
	key, value, ok := strings.Cut(spec, "=")
	if !ok {
		return fmt.Errorf("expected key=value, got %q", spec)
	}
	key = strings.TrimSpace(key)
	if err := validateDottedKey(key); err != nil {
		return err
	}
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("override %q has an empty value", key)
	}
	if o.values == nil {
		o.values = make(map[string]string)
	}
	o.values[key] = strings.TrimSpace(value)
	return nil
}

// String 实现 flag.Value。
func (o *Overrides) String() string {
	if o == nil || len(o.values) == 0 {
		return ""
	}
	keys := make([]string, 0, len(o.values))
	for key := range o.values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+o.values[key])
	}
	return strings.Join(parts, ",")
}

// Get 报告某个覆盖键的原始值。
func (o *Overrides) Get(key string) (string, bool) {
	if o == nil {
		return "", false
	}
	value, ok := o.values[key]
	return value, ok
}

// lookup 返回与给定任一键匹配的第一条覆盖值。
func (o *Overrides) lookup(keys ...string) (string, bool) {
	for _, key := range keys {
		if value, ok := o.Get(key); ok {
			return value, true
		}
	}
	return "", false
}

// All 返回原始覆盖 map 的副本。
func (o *Overrides) All() map[string]string {
	out := make(map[string]string, len(o.values))
	for key, value := range o.values {
		out[key] = value
	}
	return out
}

// Nested 把点分覆盖值转换为嵌套 map，在可能的情况下把值解析为 JSON。
// 这正是配置文件写入器所消费的形式。
func (o *Overrides) Nested() map[string]any {
	out := make(map[string]any, len(o.values))
	for key, value := range o.values {
		setDotted(out, key, parseValue(value))
	}
	return out
}

func validateDottedKey(key string) error {
	if key == "" {
		return errors.New("override has an empty key")
	}
	for _, segment := range strings.Split(key, ".") {
		if strings.TrimSpace(segment) == "" {
			return fmt.Errorf("invalid dotted key %q", key)
		}
	}
	return nil
}

// parseValue 把 CLI/配置值解码为 JSON，失败时回退为字符串。
func parseValue(raw string) any {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	var decoded any
	if err := json.Unmarshal([]byte(trimmed), &decoded); err == nil {
		return decoded
	}
	return raw
}

// stringList 是可重复的 string flag（--add-dir、--plugins）。
type stringList struct {
	values []string
}

func (l *stringList) String() string { return strings.Join(l.values, ",") }

func (l *stringList) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("expected a non-empty value")
	}
	l.values = append(l.values, value)
	return nil
}

// optionalString 是值可选的 flag：单独的 `--worktree` 表示
// "auto"，`--worktree=name` 则指定 worktree 名称。
type optionalString struct {
	value string
}

func (o *optionalString) String() string { return o.value }

func (o *optionalString) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" || value == "true" {
		value = "auto"
	}
	o.value = value
	return nil
}

// IsBoolFlag 让 flag 包接受裸的 --worktree。
func (o *optionalString) IsBoolFlag() bool { return true }

// flagValues 为每个注册的 flag 保存一个指针。它存在的意义是让 flag 集合
// 只有唯一一处定义，供 parity 测试检查。
type flagValues struct {
	model    *string
	baseURL  *string
	apiKey   *string
	protocol *string
	provider *string
	profile  *string

	yolo        *bool
	maxTurns    *int
	maxParallel *int
	mode        *string
	cwd         *string

	sandboxMode *string
	approval    *string
	session     *string
	resume      *bool
	undo        *bool
	retry       *bool
	title       *string
	locale      *string

	jsonLines     *bool
	listProfiles  *bool
	listSessions  *bool
	listModels    *bool
	noColor       *bool
	tokens        *bool
	trustProject  *bool
	thinking      *string
	noAutoCompact *bool
	noPromptCache *bool
	theme         *string
	plugins       *stringList

	maxTokens      *int
	maxCost        *float64
	maxSeconds     *int
	priceInput     *float64
	priceOutput    *float64
	fallbackModel  *string
	secondaryModel *string
	addDirs        *stringList
	outputSchema   *string

	background      *bool
	worktree        *optionalString
	enableBrowser   *bool
	enableDesktop   *bool
	enableService   *bool
	workflowMode    *string
	workflowResume  *bool
	workflowJournal *string
	gui             *bool
	tui             *bool

	serve *bool
	rpc   *bool
	acp   *bool
	addr  *string
	port  *int
	token *string

	version *bool
	help    *bool

	overrides *Overrides
}
