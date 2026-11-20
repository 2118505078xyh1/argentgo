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

// TestFindAssetIsDeterministicForAmbiguousNames 是针对某个 bug 的回归测试：
// longestAlias 曾以随机顺序遍历 map。一个名字若带有来自不同键的两个等长别名
// （".linux." 和 ".win32." 都是七个
// 字节），过去会在不同调用间时而匹配时而不匹配，因此同一
// 版本在不同进程中可能安装不同的资源。
func TestFindAssetIsDeterministicForAmbiguousNames(t *testing.T) {
	assets := []Asset{
		{Name: "argent_1.2.3_linux_win32.zip", URL: "ambiguous"},
		{Name: "argent_1.2.3_windows_amd64.zip", URL: "windows"},
	}
	for index := 0; index < 200; index++ {
		asset, found := FindAsset(assets, "windows", "amd64")
		if !found || asset.URL != "windows" {
			t.Fatalf("call %d: FindAsset = %+v, %v; the ambiguous asset must never win",
				index, asset, found)
		}
	}
}

// TestFindAssetRejectsAmbiguousArchitecture 覆盖来自不同键的等长架构
// 别名。该资源必须对两个平台都被拒绝，
// 而不是被随机选作其中之一的回退。
func TestFindAssetRejectsAmbiguousArchitecture(t *testing.T) {
	asset := Asset{Name: "argent_1.2.3_linux_amd64_arm64.tar.gz", URL: "fat"}
	for _, arch := range []string{"amd64", "arm64"} {
		if got, found := FindAsset([]Asset{asset}, "linux", arch); found {
			t.Fatalf("FindAsset(linux, %s) selected ambiguous asset %+v", arch, got)
		}
	}
}

// TestLongestAliasRejectsAmbiguousNames 直接检查这个共享辅助函数，以便
// 将来修改分类逻辑时不会悄悄重新引入 map 顺序的偶然性。
func TestLongestAliasRejectsAmbiguousNames(t *testing.T) {
	if key, matched, ambiguous := longestAlias(".argent.linux.win32.zip.", osAliases); !ambiguous || matched || key != "" {
		t.Fatalf("longestAlias(ambiguous OS) = (%q, %v, %v)", key, matched, ambiguous)
	}
	if key, matched, ambiguous := longestAlias(".argent.amd64.arm64.tar.gz.", archAliases); !ambiguous || matched || key != "" {
		t.Fatalf("longestAlias(ambiguous arch) = (%q, %v, %v)", key, matched, ambiguous)
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

// TestFindAssetRejectsForeignArchForUnknownTarget 覆盖一个有效但不在
// archAliases 中的 Go GOARCH（例如 riscv64）。明确指定
// 不同架构的资源绝不能作为仅含 OS 的回退被接受，否则
// 更新器可能在不支持的机器上安装 amd64 二进制。
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

	// 名称中不含架构的资源仍是有效的仅 OS 回退。
	generic := Asset{Name: "argent_1.2.3_linux.tar.gz", URL: "generic"}
	asset, found = FindAsset([]Asset{generic}, "linux", "riscv64")
	if !found || asset.URL != "generic" {
		t.Fatalf("FindAsset = %+v, %v; want the OS-only fallback", asset, found)
	}
}
