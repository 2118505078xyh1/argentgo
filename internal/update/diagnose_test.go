package update

import (
	"reflect"
	"testing"
)

func TestDiagnoseAssetMatchReasons(t *testing.T) {
	assets := []Asset{
		{Name: "argent_1.2.3_linux_amd64.tar.gz", URL: "u1"},
		{Name: "argent_1.2.3_windows_amd64.zip", URL: "u2"},
		{Name: "argent_1.2.3_windows_arm64.zip", URL: "u3"},
		{Name: "argent_windows_amd64.zip.sha256", URL: "u4"},
		{Name: "argent_windows_amd64.exe"},
		{Name: "argent-win64.zip", URL: "u6"},
	}
	want := []AssetMatchDiagnosis{
		{
			Name:   "argent_1.2.3_linux_amd64.tar.gz",
			Reason: AssetMatchReasonWrongOS, ForeignOS: true,
		},
		{
			Name:     "argent_1.2.3_windows_amd64.zip",
			Selected: true, Eligible: true, Reason: AssetMatchReasonSelected,
			Score: 113, OSMatched: true, ArchMatched: true,
		},
		{
			Name:      "argent_1.2.3_windows_arm64.zip",
			Reason:    AssetMatchReasonWrongArch,
			OSMatched: true, ForeignArch: true,
		},
		{
			Name:   "argent_windows_amd64.zip.sha256",
			Reason: AssetMatchReasonNoise,
		},
		{
			Name:   "argent_windows_amd64.exe",
			Reason: AssetMatchReasonNoURL,
		},
		{
			Name:     "argent-win64.zip",
			Eligible: true, Reason: AssetMatchReasonEligible,
			Score: 13, OSMatched: true,
		},
	}
	got := DiagnoseAssetMatch(assets, "windows", "amd64")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DiagnoseAssetMatch =\n%+v\nwant\n%+v", got, want)
	}
}

func TestDiagnoseAssetMatchInvalidTarget(t *testing.T) {
	assets := []Asset{{Name: "argent_windows_amd64.zip", URL: "u"}}
	for _, target := range []struct{ goos, goarch string }{
		{"", "amd64"}, {"windows", ""}, {"  ", "amd64"}, {"windows", "\t"},
	} {
		got := DiagnoseAssetMatch(assets, target.goos, target.goarch)
		want := []AssetMatchDiagnosis{{Name: assets[0].Name, Reason: AssetMatchReasonInvalidTarget}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("DiagnoseAssetMatch(%q, %q) = %+v, want %+v",
				target.goos, target.goarch, got, want)
		}
	}
}

func TestDiagnoseAssetMatchNormalizesPlatform(t *testing.T) {
	assets := []Asset{{Name: "Argent_Windows_X86_64.ZIP", URL: "u"}}
	diagnoses := DiagnoseAssetMatch(assets, " WINDOWS ", "AMD64")
	if len(diagnoses) != 1 || !diagnoses[0].Selected || !diagnoses[0].Eligible {
		t.Fatalf("normalized platform should select the asset, got %+v", diagnoses)
	}
	if second := DiagnoseAssetMatch(assets, "windows", "amd64"); !reflect.DeepEqual(diagnoses, second) {
		t.Fatal("normalization should not change the diagnosis")
	}
}

func TestDiagnoseAssetMatchAgreesWithFindAsset(t *testing.T) {
	assets := []Asset{
		{Name: "argent_1.2.3_linux_amd64.tar.gz", URL: "l64"},
		{Name: "argent_1.2.3_linux_arm64.tar.gz", URL: "la64"},
		{Name: "argent_1.2.3_windows_amd64.zip", URL: "w64"},
		{Name: "argent-win64.zip", URL: "fallback"},
		{Name: "argent_darwin_aarch64.tar.gz", URL: "d64"},
		{Name: "argent_windows_arm64.zip", URL: "wa64"},
	}
	platforms := []struct{ goos, goarch string }{
		{"windows", "amd64"},
		{"linux", "arm64"},
		{"darwin", "amd64"},
		{"freebsd", "386"},
	}
	for _, platform := range platforms {
		t.Run(platform.goos+"/"+platform.goarch, func(t *testing.T) {
			selected, found := FindAsset(assets, platform.goos, platform.goarch)
			diagnoses := DiagnoseAssetMatch(assets, platform.goos, platform.goarch)
			if len(diagnoses) != len(assets) {
				t.Fatalf("got %d diagnoses, want %d", len(diagnoses), len(assets))
			}
			selectedCount := 0
			for index, diagnosis := range diagnoses {
				if !diagnosis.Selected {
					continue
				}
				selectedCount++
				if !found || assets[index].Name != selected.Name {
					t.Fatalf("selected %q, FindAsset picked %q (found=%v)",
						assets[index].Name, selected.Name, found)
				}
			}
			wantSelected := 0
			if found {
				wantSelected = 1
			}
			if selectedCount != wantSelected {
				t.Fatalf("selected %d assets, want %d", selectedCount, wantSelected)
			}
		})
	}
}

// TestDiagnoseAssetMatchRejectsForeignArchForUnknownTarget 使诊断与
// FindAsset 在目标架构不在 archAliases 中时保持一致。
func TestDiagnoseAssetMatchRejectsForeignArchForUnknownTarget(t *testing.T) {
	assets := []Asset{
		{Name: "argent_1.2.3_linux_amd64.tar.gz", URL: "amd64"},
		{Name: "argent_1.2.3_linux_riscv64.tar.gz", URL: "riscv64"},
	}
	got := DiagnoseAssetMatch(assets, "linux", "riscv64")
	if len(got) != 2 {
		t.Fatalf("got %d diagnoses, want 2", len(got))
	}
	if got[0].Eligible || got[0].Reason != AssetMatchReasonWrongArch || !got[0].ForeignArch {
		t.Fatalf("amd64 asset diagnosis = %+v, want a foreign-arch rejection", got[0])
	}
	if !got[1].Eligible || !got[1].Selected || got[1].Reason != AssetMatchReasonSelected {
		t.Fatalf("riscv64 asset diagnosis = %+v, want it selected", got[1])
	}
}

// TestDiagnoseAssetMatchAmbiguousName 与 FindAsset 一致：用等长别名提到两个
// 平台的资源是外来资源，绝不是静默回退。
func TestDiagnoseAssetMatchAmbiguousName(t *testing.T) {
	assets := []Asset{
		{Name: "argent_1.2.3_linux_amd64_arm64.tar.gz", URL: "fat"},
		{Name: "argent_1.2.3_linux_amd64.tar.gz", URL: "linux"},
	}
	got := DiagnoseAssetMatch(assets, "linux", "amd64")
	if len(got) != 2 {
		t.Fatalf("got %d diagnoses, want 2", len(got))
	}
	if got[0].Eligible || got[0].Reason != AssetMatchReasonWrongArch || !got[0].ForeignArch {
		t.Fatalf("ambiguous asset diagnosis = %+v, want a foreign-arch rejection", got[0])
	}
	if !got[1].Eligible || !got[1].Selected {
		t.Fatalf("linux asset diagnosis = %+v, want it selected", got[1])
	}
}

func TestDiagnoseAssetMatchIsPure(t *testing.T) {
	assets := []Asset{
		{Name: "argent_1.2.3_linux_amd64.tar.gz", URL: "u"},
		{Name: "checksums.txt", URL: "c"},
	}
	before := append([]Asset(nil), assets...)
	first := DiagnoseAssetMatch(assets, "linux", "amd64")
	second := DiagnoseAssetMatch(assets, "linux", "amd64")
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("identical inputs produced different diagnoses:\n%+v\n%+v", first, second)
	}
	if !reflect.DeepEqual(assets, before) {
		t.Fatal("DiagnoseAssetMatch mutated its input")
	}
	if got := DiagnoseAssetMatch(nil, "linux", "amd64"); len(got) != 0 {
		t.Fatalf("no assets should produce no diagnoses, got %+v", got)
	}
}
