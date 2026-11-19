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
