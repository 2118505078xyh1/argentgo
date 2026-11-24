package config

import "encoding/json"

// configSchemaProperty 描述一个配置键在 JSON Schema 中的类型、约束与文档。
// 字段声明顺序固定，encoding/json 会据此稳定序列化，使其可作为编辑器
// 补全与校验所依赖的单一事实来源。
type configSchemaProperty struct {
	Type                 string                `json:"type,omitempty"`
	Title                string                `json:"title,omitempty"`
	Description          string                `json:"description,omitempty"`
	Default              any                   `json:"default,omitempty"`
	Enum                 []string              `json:"enum,omitempty"`
	Minimum              *int                  `json:"minimum,omitempty"`
	Maximum              *int                  `json:"maximum,omitempty"`
	Items                *configSchemaProperty `json:"items,omitempty"`
	AdditionalProperties *configSchemaProperty `json:"additionalProperties,omitempty"`
	Aliases              []string              `json:"x-aliases,omitempty"`
}

// configSchemaDocument 是 configSchemaJSON 输出的 JSON Schema 文档结构。
type configSchemaDocument struct {
	Schema               string                          `json:"$schema"`
	Title                string                          `json:"title"`
	Description          string                          `json:"description"`
	Type                 string                          `json:"type"`
	Properties           map[string]configSchemaProperty `json:"properties"`
	AdditionalProperties bool                            `json:"additionalProperties"`
}

// configSchemaInt 把整数包装成指针，用于表达可能为零的下界与上界，
// 避免 omitempty 把合法的零值边界丢弃。
func configSchemaInt(value int) *int {
	return &value
}

// configSchemaJSON 生成 argentgo 配置文件的 JSON Schema 字节。
//
// 这是主要配置键的单一事实来源：每个键的类型、默认值、取值范围、别名
// 以及中文说明都集中在此，编辑器可据此做补全与校验。返回值是确定性的：
// 属性按名称排序（encoding/json 对 map 键排序），缩进与字段顺序固定；
// 中文以原始 UTF-8 写出，不转义成 \uXXXX。这里构造的是固定结构，
// 编码不会失败；一旦失败则返回 nil。
func configSchemaJSON() []byte {
	props := map[string]configSchemaProperty{
		"model": {
			Type:        "string",
			Title:       "模型",
			Description: "模型标识，决定把聊天补全请求发往哪个模型端点。",
			Default:     defaultModel,
			Aliases:     []string{"modelName"},
		},
		"baseURL": {
			Type:        "string",
			Title:       "接口地址",
			Description: "模型 API 的根地址。",
			Default:     defaultBaseURL,
			Aliases:     []string{"baseUrl", "base_url"},
		},
		"apiKey": {
			Type:        "string",
			Title:       "API 凭据",
			Description: "模型 API 的密钥；留空时由提供方的凭据链提供。",
			Default:     "",
			Aliases:     []string{"api_key"},
		},
		"protocol": {
			Type:        "string",
			Title:       "协议",
			Description: "与模型通信使用的协议。",
			Default:     defaultProtocol,
		},
		"locale": {
			Type:        "string",
			Title:       "语言",
			Description: "界面文案使用的语言，例如 zh-CN 或 en-US。",
			Default:     defaultLocale,
		},
		"sandboxMode": {
			Type:        "string",
			Title:       "沙箱模式",
			Description: "文件工具的安全模式，控制修改操作的工作区边界。",
			Default:     SandboxWorkspaceWrite,
			Enum:        []string{SandboxReadOnly, SandboxWorkspaceWrite, SandboxDangerFullAccess},
			Aliases:     []string{"sandbox_mode"},
		},
		"privacyMode": {
			Type:        "string",
			Title:       "网络姿态",
			Description: "网络访问控制：local-only 会阻止远程端点与 web 工具。",
			Default:     PrivacyModeAny,
			Enum:        []string{PrivacyModeAny, PrivacyModeLocalOnly},
			Aliases:     []string{"privacy_mode"},
		},
		"maxTurns": {
			Type:        "integer",
			Title:       "最大轮数",
			Description: "单次任务允许的最大模型往返轮数。",
			Default:     defaultMaxTurns,
			Minimum:     configSchemaInt(0),
			Aliases:     []string{"max_turns"},
		},
		"yolo": {
			Type:        "boolean",
			Title:       "免审批",
			Description: "是否跳过危险操作的审批确认。",
			Default:     false,
		},
		"storageDir": {
			Type:        "string",
			Title:       "存储目录",
			Description: "会话与状态文件的存放目录；默认位于用户主目录下的 .argentgo。",
			Aliases:     []string{"storage_dir"},
		},
		"provider": {
			Type:        "string",
			Title:       "提供方",
			Description: "选定的提供方模板名称。",
			Default:     "",
		},
		"profile": {
			Type:        "string",
			Title:       "配置文件档位",
			Description: "要激活的 profile 名称。",
			Default:     "",
			Aliases:     []string{"activeProfile"},
		},
		"verifyCommand": {
			Type:        "array",
			Title:       "校验命令",
			Description: "编辑后运行的校验命令及其参数；为空则禁用校验。",
			Items:       &configSchemaProperty{Type: "string"},
			Aliases:     []string{"verify_command"},
		},
		"verifyCommands": {
			Type:                 "object",
			Title:                "按语言分组的校验命令",
			Description:          "把语言或文件扩展名映射到以空格分隔的命令字符串。",
			AdditionalProperties: &configSchemaProperty{Type: "string"},
			Aliases:              []string{"verify_commands"},
		},
		"verifyTimeoutMS": {
			Type:        "integer",
			Title:       "校验超时",
			Description: "单个校验进程的超时（毫秒），上限 120000；零表示使用默认值。",
			Default:     defaultVerifyTimeoutMS,
			Minimum:     configSchemaInt(0),
			Maximum:     configSchemaInt(120000),
			Aliases:     []string{"verify_timeout_ms"},
		},
		"verifyMaxReflections": {
			Type:        "integer",
			Title:       "最大反思轮次",
			Description: "校验失败后允许的最大反思轮次；零表示采用循环自身的默认值 3。",
			Minimum:     configSchemaInt(0),
			Aliases:     []string{"verify_max_reflections"},
		},
		"contextWindow": {
			Type:        "integer",
			Title:       "上下文窗口",
			Description: "模型上下文窗口的 token 数；零表示未指定，调用方回退到固定阈值。",
			Minimum:     configSchemaInt(0),
			Aliases:     []string{"contextWindowTokens", "context_window"},
		},
		"stopTokenIds": {
			Type:        "array",
			Title:       "停止标识",
			Description: "额外的停止 token id 列表；为空时不改变默认的停止逻辑。",
			Items:       &configSchemaProperty{Type: "integer"},
			Aliases:     []string{"stop_token_ids"},
		},
		"autoCondenseContextPercent": {
			Type:        "integer",
			Title:       "自动压缩百分比",
			Description: "触发自动压缩的上下文窗口百分比：-1 或 0 到 100。",
			Minimum:     configSchemaInt(-1),
			Maximum:     configSchemaInt(100),
			Aliases:     []string{"auto_condense_context_percent"},
		},
		"profiles": {
			Type:        "object",
			Title:       "档位集合",
			Description: "内联的 profile 定义，以名称为键。",
		},
		"providers": {
			Type:        "object",
			Title:       "提供方集合",
			Description: "自定义的提供方模板，以名称为键。",
		},
		"net": {
			Type:        "object",
			Title:       "网络出口策略",
			Description: "声明式网络出口规则集。规则字段：decision（allow/ask/deny）、hostGlob（主机通配）、port、protocol、method、requireHuman。多条命中取最严格判定，内置私网与云元数据拒绝不可被放宽。",
		},
		"key_aliases": {
			Type:        "object",
			Title:       "TUI 键位覆盖",
			Description: "把 TUI 动作重绑到新键：动作名 → 规范键串或键串数组，如 {\"undo\": \"ctrl+z\", \"scroll_up\": [\"k\", \"up\"]}。也接受 keybindings / key_bindings 作为别名块名。未配置时按键位默认值，零字节影响。",
		},
	}

	doc := configSchemaDocument{
		Schema:               "https://json-schema.org/draft/2020-12/schema",
		Title:                "argentgo 配置",
		Description:          "argentgo 的分层配置文件；未知键会被保留而不会报错。",
		Type:                 "object",
		Properties:           props,
		AdditionalProperties: true,
	}

	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil
	}
	return data
}
