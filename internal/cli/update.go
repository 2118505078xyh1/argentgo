package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/update"
)

// defaultUpdateRepo 是 argentgo 发布版本所在的位置。
const defaultUpdateRepo = "2118505078xyh1/argent"

// UpdateOps 是 update 命令驱动的接缝。生产环境保持为 nil，使每个操作都调用
// internal/update；测试注入假对象以避免网络访问。
type UpdateOps struct {
	Check    func(context.Context, string, string, *http.Client) (update.Release, bool, error)
	Download func(context.Context, update.Asset, string, string) error
	Apply    func(string, string) error
	Checksum func(context.Context, update.Release, update.Asset, *http.Client) (string, error)
}

func (a App) updater() UpdateOps {
	ops := UpdateOps{
		Check:    update.Check,
		Download: update.Download,
		Apply:    update.Apply,
		Checksum: fetchReleaseChecksum,
	}
	if a.UpdateOps != nil {
		if a.UpdateOps.Check != nil {
			ops.Check = a.UpdateOps.Check
		}
		if a.UpdateOps.Download != nil {
			ops.Download = a.UpdateOps.Download
		}
		if a.UpdateOps.Apply != nil {
			ops.Apply = a.UpdateOps.Apply
		}
		if a.UpdateOps.Checksum != nil {
			ops.Checksum = a.UpdateOps.Checksum
		}
	}
	return ops
}

// runUpdate 实现 `update [--check|--apply] [--repo owner/name] ...`。
// 不带 --apply 时只检查并打印；带上 --apply 时会下载经过校验和验证的
// 资产并安装。
func runUpdate(app App, ctx context.Context, args []string) int {
	apply := false
	asJSON := false
	repo := firstNonEmpty(app.env("ARGENT_UPDATE_REPO"), defaultUpdateRepo)
	current := app.Version
	checksum := ""
	target := ""
	assetName := ""
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "-h" || args[i] == "--help":
			updateUsage(app.IO.Out)
			return 0
		case args[i] == "--check":
			// 默认动作；接受它是为了与 --apply 对称。
		case args[i] == "--apply":
			apply = true
		case args[i] == "--json":
			asJSON = true
		case args[i] == "--repo":
			if i+1 >= len(args) {
				fmt.Fprintln(app.IO.Err, "argent: --repo needs a value")
				return 2
			}
			i++
			repo = args[i]
		case strings.HasPrefix(args[i], "--repo="):
			repo = strings.TrimPrefix(args[i], "--repo=")
		case args[i] == "--current":
			if i+1 >= len(args) {
				fmt.Fprintln(app.IO.Err, "argent: --current needs a value")
				return 2
			}
			i++
			current = args[i]
		case strings.HasPrefix(args[i], "--current="):
			current = strings.TrimPrefix(args[i], "--current=")
		case args[i] == "--sha256":
			if i+1 >= len(args) {
				fmt.Fprintln(app.IO.Err, "argent: --sha256 needs a value")
				return 2
			}
			i++
			checksum = args[i]
		case strings.HasPrefix(args[i], "--sha256="):
			checksum = strings.TrimPrefix(args[i], "--sha256=")
		case args[i] == "--target":
			if i+1 >= len(args) {
				fmt.Fprintln(app.IO.Err, "argent: --target needs a value")
				return 2
			}
			i++
			target = args[i]
		case strings.HasPrefix(args[i], "--target="):
			target = strings.TrimPrefix(args[i], "--target=")
		case args[i] == "--asset":
			if i+1 >= len(args) {
				fmt.Fprintln(app.IO.Err, "argent: --asset needs a value")
				return 2
			}
			i++
			assetName = args[i]
		case strings.HasPrefix(args[i], "--asset="):
			assetName = strings.TrimPrefix(args[i], "--asset=")
		default:
			fmt.Fprintf(app.IO.Err, "argent: unexpected argument %q\n", args[i])
			updateUsage(app.IO.Err)
			return 2
		}
	}
	if strings.TrimSpace(current) == "" {
		current = Version
	}

	client := &http.Client{Timeout: 30 * time.Second}
	ops := app.updater()
	release, newer, err := ops.Check(ctx, repo, current, client)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	if strings.TrimSpace(release.Tag) == "" {
		fmt.Fprintf(app.IO.Err, "error: no release found for %s\n", repo)
		return 1
	}
	if asJSON {
		data, err := json.MarshalIndent(map[string]any{
			"current": current,
			"latest":  release.Version(),
			"tag":     release.Tag,
			"url":     release.URL,
			"update":  newer,
			"assets":  release.Assets,
			"apply":   apply,
		}, "", "  ")
		if err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		fmt.Fprintln(app.IO.Out, string(data))
		return 0
	}
	if !apply {
		if !newer {
			fmt.Fprintf(app.IO.Out, "argentgo %s is up to date (latest %s)\n", current, release.Tag)
			return 0
		}
		fmt.Fprintf(app.IO.Out, "a newer release is available: %s (running %s)\n", release.Tag, current)
		fmt.Fprintf(app.IO.Out, "  %s\n", release.URL)
		for _, asset := range release.Assets {
			fmt.Fprintf(app.IO.Out, "  asset: %s (%d bytes)\n", asset.Name, asset.Size)
		}
		fmt.Fprintln(app.IO.Out, "  run `argent update --apply` to install it")
		return 0
	}
	if !newer {
		fmt.Fprintf(app.IO.Out, "argentgo %s is up to date; nothing to apply\n", current)
		return 0
	}
	return applyRelease(app, ctx, ops, client, release, checksum, target, assetName)
}

func applyRelease(app App, ctx context.Context, ops UpdateOps, client *http.Client, release update.Release, checksum, target, assetName string) int {
	asset, found := update.Asset{}, false
	if assetName != "" {
		for _, candidate := range release.Assets {
			if candidate.Name == assetName {
				asset, found = candidate, true
				break
			}
		}
	} else {
		asset, found = update.FindAsset(release.Assets, "", "")
	}
	if !found {
		fmt.Fprintf(app.IO.Err, "error: no release asset for this platform in %s\n", release.Tag)
		return 1
	}
	if strings.TrimSpace(checksum) == "" {
		sum, err := ops.Checksum(ctx, release, asset, client)
		if err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		checksum = sum
	}
	if strings.TrimSpace(checksum) == "" {
		fmt.Fprintf(app.IO.Err, "error: no checksum for %s; pass --sha256\n", asset.Name)
		return 1
	}

	dir := filepath.Join(app.storageDir(), "updates")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintln(app.IO.Err, "error: create update directory:", err)
		return 1
	}
	stage := filepath.Join(dir, asset.Name)
	if err := ops.Download(ctx, asset, stage, checksum); err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	// Download 已经验证校验和，但这里仍要重新检查暂存字节，使注入的或未来的
	// 下载器绝不可能替换成未经验证的二进制。zip 是例外：Download 验证归档的
	// 校验和，然后把可执行文件解包到 stage，因此 stage 的哈希不再等于已发布的
	// 归档校验和。取而代之的是确保我们绝不会把原始归档当作可执行文件安装。
	if err := verifyStagedBinary(stage, asset.Name, checksum); err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	if strings.TrimSpace(target) == "" {
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintln(app.IO.Err, "error: locate running binary:", err)
			return 1
		}
		target = exe
	}
	if err := ops.Apply(stage, target); err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	notes := filepath.Join(dir, release.Version()+"-notes.md")
	if err := os.WriteFile(notes, []byte(release.Body), 0o644); err != nil {
		fmt.Fprintln(app.IO.Err, "error: write release notes:", err)
		return 1
	}
	fmt.Fprintf(app.IO.Out, "updated argentgo to %s\n", release.Tag)
	fmt.Fprintf(app.IO.Out, "release notes: %s\n", notes)
	return 0
}

// verifyStagedBinary 对裸可执行文件的暂存文件重新校验。对于归档资产
// （zip 或 gzip 压缩的 tar），已发布的校验和覆盖的是归档，Download 已经
// 验证过它，而 stage 中存放的是解包后的可执行文件；重新计算哈希必然不匹配，
// 因此只适用归档守卫。
func verifyStagedBinary(stage, assetName, checksum string) error {
	if isArchiveAssetName(assetName) {
		return rejectArchive(stage)
	}
	return verifyChecksum(stage, checksum)
}

// isArchiveAssetName 报告 assetName 是否表示 Download 在暂存前会解包的归档。
// 它镜像 internal/update 的 zip/tar.gz 检测，使暂存二进制的重新检查与
// 下载器对哪些资产会被解包达成一致。
func isArchiveAssetName(name string) bool {
	lower := strings.ToLower(name)
	return strings.EqualFold(filepath.Ext(name), ".zip") ||
		strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz")
}

// rejectArchive 拒绝把看起来仍是压缩归档（zip 或 gzip）的文件当作可执行文件
// 安装。
func rejectArchive(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("update: open staged binary: %w", err)
	}
	defer func() { _ = file.Close() }()
	var magic [4]byte
	if _, err := io.ReadFull(file, magic[:]); err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return fmt.Errorf("update: read staged binary: %w", err)
	}
	if magic[0] == 'P' && magic[1] == 'K' &&
		((magic[2] == 3 && magic[3] == 4) ||
			(magic[2] == 5 && magic[3] == 6) ||
			(magic[2] == 7 && magic[3] == 8)) {
		return fmt.Errorf("update: staged file %s is a zip archive; refusing to install it as the executable", path)
	}
	if magic[0] == 0x1f && magic[1] == 0x8b {
		return fmt.Errorf("update: staged file %s is a gzip archive; refusing to install it as the executable", path)
	}
	return nil
}

func verifyChecksum(path, checksum string) error {
	want := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(checksum), "sha256:"))
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("update: open staged binary: %w", err)
	}
	defer func() { _ = file.Close() }()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return fmt.Errorf("update: hash staged binary: %w", err)
	}
	if got := hex.EncodeToString(hasher.Sum(nil)); got != want {
		return fmt.Errorf("update: staged binary checksum mismatch: expected %s, got %s", want, got)
	}
	return nil
}

// fetchReleaseChecksum 从发布版本的校验和资产中读取 SHA-256 摘要。
// 没有校验和资产的版本会报错：--apply 绝不猜测或跳过验证。
func fetchReleaseChecksum(ctx context.Context, release update.Release, asset update.Asset, client *http.Client) (string, error) {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	for _, candidate := range release.Assets {
		if candidate.URL == "" {
			continue
		}
		lower := strings.ToLower(candidate.Name)
		checksumFile := strings.HasSuffix(lower, ".sha256") || strings.HasSuffix(lower, ".sha256sum") ||
			strings.Contains(lower, "checksum") || strings.Contains(lower, "sha256sums")
		if !checksumFile {
			continue
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, candidate.URL, nil)
		if err != nil {
			return "", fmt.Errorf("update: build checksum request: %w", err)
		}
		response, err := client.Do(request)
		if err != nil {
			return "", fmt.Errorf("update: fetch checksum: %w", err)
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		_ = response.Body.Close()
		if err != nil {
			return "", fmt.Errorf("update: read checksum: %w", err)
		}
		if response.StatusCode/100 != 2 {
			continue
		}
		if sum := firstChecksum(string(body)); sum != "" {
			return sum, nil
		}
	}
	return "", fmt.Errorf("update: no SHA-256 checksum for %s; pass --sha256", asset.Name)
}

func firstChecksum(text string) string {
	for _, field := range strings.Fields(strings.ReplaceAll(text, ":", " ")) {
		field = strings.Trim(field, "*\r\n\t")
		if len(field) != sha256.Size*2 {
			continue
		}
		if _, err := hex.DecodeString(field); err == nil {
			return strings.ToLower(field)
		}
	}
	return ""
}

func updateUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argent update [--check] [--apply] [--repo <owner/name>] [--current <version>]
                     [--asset <name>] [--sha256 <hex>] [--target <path>]

Checks the latest GitHub release (30s timeout). --apply picks the asset for this
platform, verifies its SHA-256 checksum, installs it and writes the release
notes under the storage directory. Without --apply nothing is downloaded.
`)
}
