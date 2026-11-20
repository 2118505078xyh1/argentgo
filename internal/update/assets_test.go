package update

import (
	"runtime"
	"testing"
)

func names(assets []Asset) string {
	out := ""
	for index, asset := range assets {
		if index > 0 {
			out += ","
		}
		out += asset.Name
	}
	return out
}

func TestFindAssetTable(t *testing.T) {
	cases := []struct {
		name   string
		goos   string
		goarch string
		assets []Asset
		want   string
		found  bool
	}{
		{
			name:   "underscore naming",
			goos:   "windows",
			goarch: "amd64",
			assets: []Asset{
				{Name: "argent_1.2.3_linux_amd64.tar.gz", URL: "u1"},
				{Name: "argent_1.2.3_windows_amd64.zip", URL: "u2"},
				{Name: "sha256sums.txt", URL: "u3"},
			},
			want:  "argent_1.2.3_windows_amd64.zip",
			found: true,
		},
		{
			name:   "dash naming with x64",
			goos:   "windows",
			goarch: "amd64",
			assets: []Asset{{Name: "argent-windows-x64.zip", URL: "u"}},
			want:   "argent-windows-x64.zip",
			found:  true,
		},
		{
			name:   "x86_64 is amd64 not 386",
			goos:   "linux",
			goarch: "amd64",
			assets: []Asset{{Name: "argent_1.0.0_linux_x86_64.tar.gz", URL: "u"}},
			want:   "argent_1.0.0_linux_x86_64.tar.gz",
			found:  true,
		},
		{
			name:   "darwin zip",
			goos:   "darwin",
			goarch: "arm64",
			assets: []Asset{
				{Name: "argent_1.0.0_darwin_amd64.tar.gz", URL: "u1"},
				{Name: "argent_1.0.0_darwin_arm64.tar.gz", URL: "u2"},
			},
			want:  "argent_1.0.0_darwin_arm64.tar.gz",
			found: true,
		},
		{
			name:   "plain exe wins for windows when it is the only option",
			goos:   "windows",
			goarch: "amd64",
			assets: []Asset{{Name: "argent_windows_amd64.exe", URL: "u"}},
			want:   "argent_windows_amd64.exe",
			found:  true,
		},
		{
			name:   "zip is preferred over exe",
			goos:   "windows",
			goarch: "amd64",
			assets: []Asset{
				{Name: "argent_windows_amd64.exe", URL: "u1"},
				{Name: "argent_windows_amd64.zip", URL: "u2"},
			},
			want:  "argent_windows_amd64.zip",
			found: true,
		},
		{
			name:   "os-only asset is a fallback",
			goos:   "windows",
			goarch: "amd64",
			assets: []Asset{
				{Name: "argent_1.0.0_linux_amd64.tar.gz", URL: "u1"},
				{Name: "argent-win64.zip", URL: "u2"},
			},
			want:  "argent-win64.zip",
			found: true,
		},
		{
			name:   "wrong architecture is never chosen",
			goos:   "windows",
			goarch: "amd64",
			assets: []Asset{{Name: "argent_windows_arm64.zip", URL: "u"}},
			found:  false,
		},
		{
			name:   "wrong os is never chosen",
			goos:   "linux",
			goarch: "amd64",
			assets: []Asset{{Name: "argent_windows_amd64.zip", URL: "u"}},
			found:  false,
		},
		{
			name:   "checksum files are ignored",
			goos:   "linux",
			goarch: "arm64",
			assets: []Asset{
				{Name: "argent_linux_arm64.tar.gz.sha256", URL: "u1"},
				{Name: "SHA256SUMS", URL: "u2"},
			},
			found: false,
		},
		{
			name:   "assets without a url are ignored",
			goos:   "linux",
			goarch: "amd64",
			assets: []Asset{{Name: "argent_linux_amd64.tar.gz"}},
			found:  false,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			asset, found := FindAsset(test.assets, test.goos, test.goarch)
			if found != test.found {
				t.Fatalf("found = %v, want %v (assets: %s)", found, test.found, names(test.assets))
			}
			if found && asset.Name != test.want {
				t.Fatalf("asset = %q, want %q", asset.Name, test.want)
			}
		})
	}
}

func TestFindAssetDefaultsToTheRunningPlatform(t *testing.T) {
	assets := []Asset{
		{Name: "argent_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz", URL: "u"},
	}
	if _, found := FindAsset(assets, "", ""); !found {
		t.Fatalf("no asset matched %s/%s", runtime.GOOS, runtime.GOARCH)
	}
}

// TestFindAssetRejectsForeignArchForUnknownTarget covers a valid Go GOARCH that
// is not in archAliases (for example riscv64). An asset that explicitly names a
// different architecture must never be accepted as an OS-only fallback, or the
// updater can install an amd64 binary on an unsupported machine.
func TestFindAssetRejectsForeignArchForUnknownTarget(t *testing.T) {
	amd64 := Asset{Name: "argent_1.2.3_linux_amd64.tar.gz", URL: "amd64"}
	if _, found := FindAsset([]Asset{amd64}, "linux", "riscv64"); found {
		t.Fatal("FindAsset selected an amd64 asset for an unknown riscv64 target")
	}

	riscv := Asset{Name: "argent_1.2.3_linux_riscv64.tar.gz", URL: "riscv64"}
	asset, found := FindAsset([]Asset{amd64, riscv}, "linux", "riscv64")
	if !found || asset.URL != "riscv64" {
		t.Fatalf("FindAsset = %+v, %v; want the riscv64 asset", asset, found)
	}

	// An asset with no architecture in its name remains a valid OS-only fallback.
	generic := Asset{Name: "argent_1.2.3_linux.tar.gz", URL: "generic"}
	asset, found = FindAsset([]Asset{generic}, "linux", "riscv64")
	if !found || asset.URL != "generic" {
		t.Fatalf("FindAsset = %+v, %v; want the OS-only fallback", asset, found)
	}
}
