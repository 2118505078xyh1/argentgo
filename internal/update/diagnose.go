package update

import "strings"

// AssetMatchReason 是对某个资源如何针对目标平台分类的、
// 稳定且机器可比较的说明。
type AssetMatchReason string

const (
	// AssetMatchReasonSelected 标记 FindAsset 会选中的唯一资源。
	AssetMatchReasonSelected AssetMatchReason = "selected"
	// AssetMatchReasonEligible 是可用但在评分中落败的资源。
	AssetMatchReasonEligible AssetMatchReason = "eligible"
	// AssetMatchReasonNoURL 表示该资源无法下载。
	AssetMatchReasonNoURL AssetMatchReason = "no-url"
	// AssetMatchReasonNoise 表示该资源是校验和、签名或元数据文件。
	AssetMatchReasonNoise AssetMatchReason = "noise"
	// AssetMatchReasonWrongOS 表示该资源指定了另一个（或未指定）操作系统。
	AssetMatchReasonWrongOS AssetMatchReason = "wrong-os"
	// AssetMatchReasonWrongArch 表示该资源指定了不同的架构。
	AssetMatchReasonWrongArch AssetMatchReason = "wrong-arch"
	// AssetMatchReasonInvalidTarget 表示 goos 或 goarch 为空。
	AssetMatchReasonInvalidTarget AssetMatchReason = "invalid-target"
)

// AssetMatchDiagnosis 是单个资源的只读结论。这些布尔值
// 报告 FindAsset 所做的检查；Reason 将它们归纳为一个标签。
type AssetMatchDiagnosis struct {
	Name        string
	Selected    bool
	Eligible    bool
	Reason      AssetMatchReason
	Score       int
	OSMatched   bool
	ArchMatched bool
	ForeignOS   bool
	ForeignArch bool
}

// DiagnoseAssetMatch 按传入顺序解释每个资源。
// FindAsset 会选中的资源被标记为 Selected；平局按同样的
// 方式解决（第一个胜出）。与 FindAsset 不同，它从不替换 runtime.GOOS 或
// runtime.GOARCH，因此相同参数总是产生相同输出。
func DiagnoseAssetMatch(assets []Asset, goos, goarch string) []AssetMatchDiagnosis {
	goos = strings.ToLower(strings.TrimSpace(goos))
	goarch = strings.ToLower(strings.TrimSpace(goarch))
	out := make([]AssetMatchDiagnosis, 0, len(assets))
	if goos == "" || goarch == "" {
		for _, asset := range assets {
			out = append(out, AssetMatchDiagnosis{
				Name: asset.Name, Reason: AssetMatchReasonInvalidTarget,
			})
		}
		return out
	}

	selected, found := FindAsset(assets, goos, goarch)
	marked := false
	for _, asset := range assets {
		info := inspectAsset(asset, goos, goarch)
		diagnosis := AssetMatchDiagnosis{
			Name:        asset.Name,
			Eligible:    info.eligible,
			Reason:      info.reason,
			Score:       info.score,
			OSMatched:   info.osMatched,
			ArchMatched: info.archMatched,
			ForeignOS:   info.foreignOS,
			ForeignArch: info.foreignArch,
		}
		if found && !marked && info.eligible &&
			asset.Name == selected.Name && asset.URL == selected.URL {
			diagnosis.Selected = true
			diagnosis.Reason = AssetMatchReasonSelected
			marked = true
		}
		out = append(out, diagnosis)
	}
	return out
}
