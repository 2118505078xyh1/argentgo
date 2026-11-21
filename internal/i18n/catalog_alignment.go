package i18n

import "sort"

// KeyAlignment 描述 en-US 与 zh-CN 两种语言环境的键集合差异。
//
// 本包的文案表把两种语言放在同一条目里，因此"某个键只在一种语言中
// 有文本"既是该语言的缺失键，也是另一种语言的多余键。为避免重复状态，
// 这里只按缺失方向记录；调用方据此同时得到缺失与多余两种视角：
// MissingInZh 中的键是 en-US 的多余键，MissingInEn 中的键是 zh-CN 的
// 多余键。两个切片已排序。
type KeyAlignment struct {
	MissingInEn []string // zh-CN 有文本而 en-US 没有的键
	MissingInZh []string // en-US 有文本而 zh-CN 没有的键
}

// Aligned 报告两种语言环境的键集合是否完全一致。
func (a KeyAlignment) Aligned() bool {
	return len(a.MissingInEn) == 0 && len(a.MissingInZh) == 0
}

// Problems 以稳定顺序返回可读的问题描述，供 doctor 命令或测试输出。
// 每条同时点明缺失方与多余方，使"缺失/多余 key"都能被看到。
func (a KeyAlignment) Problems() []string {
	out := make([]string, 0, len(a.MissingInEn)+len(a.MissingInZh))
	for _, key := range a.MissingInEn {
		out = append(out, "en-US missing key (extra in zh-CN): "+key)
	}
	for _, key := range a.MissingInZh {
		out = append(out, "zh-CN missing key (extra in en-US): "+key)
	}
	return out
}

// localeTexts 是两种语言环境文案的最小视图，用来统一检查结构不同但
// 都持有 en/zh 文本的文案表（主目录与错误码表）。
type localeTexts struct {
	en string
	zh string
}

// compareKeySets 比较两个任意键集合并返回差异。它是纯函数，也是一致性
// 检查的核心：测试通过它模拟缺失键与多余键，无需改动真实文案表。
func compareKeySets(en, zh map[string]struct{}) KeyAlignment {
	missingInEn := make([]string, 0)
	missingInZh := make([]string, 0)
	for key := range zh {
		if _, ok := en[key]; !ok {
			missingInEn = append(missingInEn, key)
		}
	}
	for key := range en {
		if _, ok := zh[key]; !ok {
			missingInZh = append(missingInZh, key)
		}
	}
	sort.Strings(missingInEn)
	sort.Strings(missingInZh)
	return KeyAlignment{MissingInEn: missingInEn, MissingInZh: missingInZh}
}

// checkKeyAlignment 从一份键值表中派生两种语言的键集合并比较。
// 只有某语言文本非空的键才计入该语言的键集合；两种语言都为空的键
// 不计入任何一侧（它属于文本缺失而非键集合不齐，由 AuditTranslations
// 报告）。
func checkKeyAlignment(table map[string]localeTexts) KeyAlignment {
	enKeys := make(map[string]struct{})
	zhKeys := make(map[string]struct{})
	for key, text := range table {
		if text.en != "" {
			enKeys[key] = struct{}{}
		}
		if text.zh != "" {
			zhKeys[key] = struct{}{}
		}
	}
	return compareKeySets(enKeys, zhKeys)
}

// AuditCatalogKeyAlignment 检查主 UI 文案表（catalog）的键对齐。
// 它是纯函数：只读嵌入的表，不改变 T 的查找行为，也不触碰 Missing 日志。
func AuditCatalogKeyAlignment() KeyAlignment {
	return checkKeyAlignment(catalogView())
}

// AuditErrorMessagesKeyAlignment 检查错误码文案表（errorMessageTable）的键对齐。
// 它是纯函数：只读嵌入的表，不改变 errorMessage 的查找行为。
func AuditErrorMessagesKeyAlignment() KeyAlignment {
	return checkKeyAlignment(errorMessagesView())
}

// namedKeyAlignment 把一份键对齐结果与其来源表名绑定，供聚合审计在明细中
// 标注来源；Table 复用占位符审计的稳定标识，使两类明细可按表统一过滤。
type namedKeyAlignment struct {
	name  string
	align KeyAlignment
}

// messageTableViews 返回四张独立消息表的 en/zh 视图，顺序固定。
// 它们与 catalog 结构不同（各自的 entry 类型），但都持有 en/zh 文本，
// 统一转成 localeTexts 即可复用键对齐与占位符检查，无需改动任何文案字节。
func messageTableViews() []placeholderTable {
	return []placeholderTable{
		{name: placeholderTableCommandMessages, entries: commandMessagesView()},
		{name: placeholderTableReliabilityMessages, entries: reliabilityMessagesView()},
		{name: placeholderTableOptInMessages, entries: optInMessagesView()},
		{name: placeholderTableErrorHintMessages, entries: errorHintMessagesView()},
	}
}

// AuditMessageTablesKeyAlignment 检查四张独立消息表的键对齐，按固定顺序
// 返回带表名的结果。它是纯函数：只读嵌入的表，不改变任何查找行为或文案字节。
func AuditMessageTablesKeyAlignment() []namedKeyAlignment {
	tables := messageTableViews()
	out := make([]namedKeyAlignment, 0, len(tables))
	for _, table := range tables {
		out = append(out, namedKeyAlignment{name: table.name, align: checkKeyAlignment(table.entries)})
	}
	return out
}

// 以下四个 view 把各自的 entry 类型投影为统一的 localeTexts，
// 使四张消息表能复用 catalog/errorMessage 的键对齐与占位符审计逻辑。
func commandMessagesView() map[string]localeTexts {
	view := make(map[string]localeTexts, len(commandMessageTable))
	for key, entry := range commandMessageTable {
		view[key] = localeTexts{en: entry.en, zh: entry.zh}
	}
	return view
}

func reliabilityMessagesView() map[string]localeTexts {
	view := make(map[string]localeTexts, len(reliabilityMessageTable))
	for key, entry := range reliabilityMessageTable {
		view[key] = localeTexts{en: entry.en, zh: entry.zh}
	}
	return view
}

func optInMessagesView() map[string]localeTexts {
	view := make(map[string]localeTexts, len(optInMessageTable))
	for key, entry := range optInMessageTable {
		view[key] = localeTexts{en: entry.en, zh: entry.zh}
	}
	return view
}

func errorHintMessagesView() map[string]localeTexts {
	view := make(map[string]localeTexts, len(errorHintMessageTable))
	for key, entry := range errorHintMessageTable {
		view[key] = localeTexts{en: entry.en, zh: entry.zh}
	}
	return view
}

func catalogView() map[string]localeTexts {
	view := make(map[string]localeTexts, len(catalog))
	for key, entry := range catalog {
		view[key] = localeTexts{en: entry.en, zh: entry.zh}
	}
	return view
}

func errorMessagesView() map[string]localeTexts {
	view := make(map[string]localeTexts, len(errorMessageTable))
	for key, entry := range errorMessageTable {
		view[key] = localeTexts{en: entry.en, zh: entry.zh}
	}
	return view
}
