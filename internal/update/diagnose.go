package update

import "strings"

// AssetMatchReason is a stable, machine-comparable explanation of how an asset
// was classified for a target platform.
type AssetMatchReason string

const (
	// AssetMatchReasonSelected marks the single asset FindAsset would pick.
	AssetMatchReasonSelected AssetMatchReason = "selected"
	// AssetMatchReasonEligible is a usable asset that lost on score.
	AssetMatchReasonEligible AssetMatchReason = "eligible"
	// AssetMatchReasonNoURL means the asset cannot be downloaded.
	AssetMatchReasonNoURL AssetMatchReason = "no-url"
	// AssetMatchReasonNoise means the asset is a checksum, signature or metadata file.
	AssetMatchReasonNoise AssetMatchReason = "noise"
	// AssetMatchReasonWrongOS means the asset names another (or no) operating system.
	AssetMatchReasonWrongOS AssetMatchReason = "wrong-os"
	// AssetMatchReasonWrongArch means the asset names a different architecture.
	AssetMatchReasonWrongArch AssetMatchReason = "wrong-arch"
	// AssetMatchReasonInvalidTarget means goos or goarch was empty.
	AssetMatchReasonInvalidTarget AssetMatchReason = "invalid-target"
)

// AssetMatchDiagnosis is the read-only verdict for one asset. The booleans
// report the checks FindAsset performs; Reason collapses them into one label.
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

// DiagnoseAssetMatch explains every asset in the same order it was given. The
// asset FindAsset would select is marked Selected; ties are resolved the same
// way (first wins). Unlike FindAsset it never substitutes runtime.GOOS or
// runtime.GOARCH, so identical arguments always produce identical output.
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
