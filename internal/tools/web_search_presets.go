package tools

import "strings"

// SearchPreset 描述一种可填入 ARGENT_WEB_SEARCH_URL 的搜索后端预设。
//
// 预设只携带元信息，绝不发起请求或读取环境变量：它仅服务于 doctor 提示
// 与后续可选的接线。没有用户提供的 API key 或自托管实例时，web_search
// 不会因此获得联网能力。
type SearchPreset struct {
	Name           string
	URL            string
	HeaderName     string
	HeaderValueEnv string
	Note           string
}

// SearchPresets 是内置的搜索后端预设。顺序固定为 Brave、Tavily、SearXNG，
// 便于 doctor 与文档稳定展示。SearXNG 为自托管，没有固定官方端点。
var SearchPresets = []SearchPreset{
	{
		Name:           "brave",
		URL:            "https://api.search.brave.com/res/v1/web/search",
		HeaderName:     "X-Subscription-Token",
		HeaderValueEnv: "BRAVE_API_KEY",
		Note:           "Brave 搜索需自行申请 API key：设置环境变量 BRAVE_API_KEY，并把 ARGENT_WEB_SEARCH_URL 指向 https://api.search.brave.com/res/v1/web/search。未配置 key 时无法联网。",
	},
	{
		Name:           "tavily",
		URL:            "https://api.tavily.com/search",
		HeaderValueEnv: "TAVILY_API_KEY",
		Note:           "Tavily 搜索需自行申请 API key：设置环境变量 TAVILY_API_KEY，并把 ARGENT_WEB_SEARCH_URL 指向 https://api.tavily.com/search。其请求与返回体形态和默认端点不同（key 经请求体传递，无自定义请求头），但 web_search 解析器可容忍多种结果封装。未配置 key 时无法联网。",
	},
	{
		Name: "searxng",
		Note: "SearXNG 需自行部署实例（无需 API key）：把 ARGENT_WEB_SEARCH_URL 指向你的实例地址，例如 https://searx.example.org/search。未部署实例时无法联网。",
	},
}

// searchPresetAliases 是名称到规范预设名的映射。键一律小写，
// ResolveSearchPreset 会先对输入做 TrimSpace 与 ToLower 再查表。
var searchPresetAliases = map[string]string{
	"brave":          "brave",
	"brave_search":   "brave",
	"bravesearch":    "brave",
	"tavily":         "tavily",
	"tavily_search":  "tavily",
	"tavilysearch":   "tavily",
	"searx":          "searxng",
	"searxng":        "searxng",
	"searxng_search": "searxng",
}

// ResolveSearchPreset 按名称（大小写不敏感、支持别名）解析搜索预设。
// 未知名称返回零值与 false。函数只读全局数据，不修改任何状态。
func ResolveSearchPreset(name string) (SearchPreset, bool) {
	canonical, ok := searchPresetAliases[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return SearchPreset{}, false
	}
	for _, preset := range SearchPresets {
		if preset.Name == canonical {
			return preset, true
		}
	}
	return SearchPreset{}, false
}

// PresetForEnv 依据注入的环境变量推断当前应使用的搜索后端预设。
// 它只读 env，不修改任何全局状态；识别失败时返回零值与 false（fail-closed），
// 调用方据此保持默认请求形态。判断顺序：
//  1. ARGENT_WEB_SEARCH_PROVIDER 显式指定且能解析为已知预设时直接采用；
//  2. 未显式指定时，按 SearchPresets 的稳定顺序查找已配置的 key，
//     命中第一个带 key 的预设（Brave 优先于 Tavily）；
//  3. 都没有则返回 false。
//
// 名称与 key 均用 TrimSpace/ToLower 做 Unicode 安全的清洗，避免字节切片
// 在多字节字符（如 CJK）中间截断；空白 key 视为未配置。
func PresetForEnv(env func(string) string) (SearchPreset, bool) {
	if provider := strings.TrimSpace(env(webSearchProviderEnv)); provider != "" {
		preset, ok := ResolveSearchPreset(provider)
		if !ok {
			return SearchPreset{}, false
		}
		return preset, true
	}
	for _, preset := range SearchPresets {
		if preset.HeaderValueEnv == "" {
			continue
		}
		if strings.TrimSpace(env(preset.HeaderValueEnv)) != "" {
			return preset, true
		}
	}
	return SearchPreset{}, false
}

// searchPresetNames 返回规范预设名的稳定展示串。
// 这里写死顺序而不遍历 SearchPresets，避免调用方改动该切片后提示顺序漂移。
func searchPresetNames() string {
	return "brave / tavily / searxng"
}

// SearchPresetNote 返回稳定的中文提示。已知预设返回其专属说明；
// 未知名称或未配置时返回通用提示，引导用户设置端点或 API key。
func SearchPresetNote(name string) string {
	if preset, ok := ResolveSearchPreset(name); ok {
		return preset.Note
	}
	return "web_search 默认不内置任何搜索后端，未配置时不会联网：请设置 ARGENT_WEB_SEARCH_URL 指向自托管端点（如自建 SearXNG），或设置 BRAVE_API_KEY / TAVILY_API_KEY 后选用相应预设。可用预设：" + searchPresetNames() + "。"
}
