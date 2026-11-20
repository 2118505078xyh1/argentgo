// Package update 检查 GitHub 发布以获取更新的 argent 构建，下载
// 带校验和的合适资源，并原子地替换正在运行的二进制文件。
//
// 仅使用标准库：发布元数据来自
// /releases/latest，资源是普通 HTTP 下载，替换是一次重命名。
package update

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	releaseBodyLimit = 4 << 20
	userAgent        = "argentgo-updater"
)

// apiBase 是变量，以便测试（以及 GitHub Enterprise 安装）可以把
// 客户端指向别处。
var apiBase = "https://api.github.com"

// Asset 是一个可下载的发布文件。
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

// Release 是 argent 使用的 GitHub 发布负载的一个子集。
type Release struct {
	Repo        string    `json:"-"`
	Tag         string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	URL         string    `json:"html_url"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []Asset   `json:"assets"`
}

// Version 是去掉前导 "v" 的标签。
func (r Release) Version() string { return strings.TrimPrefix(r.Tag, "v") }

// Check 获取 repo（"owner/name"）的最新发布，并报告它
// 是否比 currentVersion 更新。nil 客户端使用 30 秒默认值。
func Check(ctx context.Context, repo, currentVersion string, client *http.Client) (Release, bool, error) {
	var release Release
	if strings.Count(repo, "/") != 1 || strings.ContainsAny(repo, " \t") ||
		strings.HasPrefix(repo, "/") || strings.HasSuffix(repo, "/") {
		return release, false, fmt.Errorf("update: repo must look like owner/name, got %q", repo)
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimSuffix(apiBase, "/")+"/repos/"+repo+"/releases/latest", nil)
	if err != nil {
		return release, false, fmt.Errorf("update: build the release request: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", userAgent)
	response, err := client.Do(request)
	if err != nil {
		return release, false, fmt.Errorf("update: fetch the latest release: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, releaseBodyLimit))
	if err != nil {
		return release, false, fmt.Errorf("update: read the release response: %w", err)
	}
	if response.StatusCode == http.StatusNotFound {
		return release, false, fmt.Errorf("update: %s has no published releases", repo)
	}
	if response.StatusCode/100 != 2 {
		return release, false, fmt.Errorf("update: release lookup returned HTTP %d: %s",
			response.StatusCode, snippet(body))
	}
	if err := json.Unmarshal(body, &release); err != nil {
		return release, false, fmt.Errorf("update: parse the release response: %w", err)
	}
	release.Repo = repo
	if release.Tag == "" {
		return release, false, errors.New("update: release has no tag_name")
	}
	return release, CompareVersions(release.Tag, currentVersion) > 0, nil
}

func snippet(body []byte) string {
	text := strings.TrimSpace(string(body))
	if len(text) > 200 {
		return text[:200] + "…"
	}
	return text
}

// Version 是一个语义化版本。构建元数据在解析时被丢弃，因此
// v1.2.3+build.5 和 v1.2.3 比较相等。
type Version struct {
	Major int
	Minor int
	Patch int
	// Pre 保存预发布标识符；正式发布时为空。
	Pre []string
}

// ParseVersion 接受 "1.2.3"、"v1.2.3"、"1.2" 以及类似
// "1.0.0-beta.2" 的预发布后缀。"+" 之后的构建元数据被忽略。带前导零的
// 数字预发布标识符（例如 "1.0.0-01"）会被拒绝。
func ParseVersion(text string) (Version, error) {
	original := strings.TrimSpace(text)
	raw := strings.TrimPrefix(strings.TrimPrefix(original, "v"), "V")
	if raw == "" {
		return Version{}, fmt.Errorf("update: empty version %q", original)
	}
	if index := strings.IndexByte(raw, '+'); index >= 0 {
		raw = raw[:index]
	}
	var prerelease []string
	core := raw
	if index := strings.IndexByte(raw, '-'); index >= 0 {
		core = raw[:index]
		rest := raw[index+1:]
		if rest == "" {
			return Version{}, fmt.Errorf("update: %q has an empty prerelease", original)
		}
		prerelease = strings.Split(rest, ".")
		for _, identifier := range prerelease {
			if identifier == "" {
				return Version{}, fmt.Errorf("update: %q has an empty prerelease identifier", original)
			}
			// SemVer 2.0.0 §9：数字标识符不得包含前导零。
			if isNumericIdentifier(identifier) && len(identifier) > 1 && identifier[0] == '0' {
				return Version{}, fmt.Errorf("update: %q has a prerelease identifier with a leading zero: %q",
					original, identifier)
			}
		}
	}
	parts := strings.Split(core, ".")
	if len(parts) == 0 || len(parts) > 3 {
		return Version{}, fmt.Errorf("update: %q is not a semantic version", original)
	}
	values := [3]int{}
	for index, part := range parts {
		value, err := strconv.Atoi(part)
		if err != nil || value < 0 {
			return Version{}, fmt.Errorf("update: %q is not a semantic version", original)
		}
		values[index] = value
	}
	return Version{Major: values[0], Minor: values[1], Patch: values[2], Pre: prerelease}, nil
}

// Compare 按 semver 优先级规则对两个版本排序：正式版
// 高于其任何预发布版，数字标识符按数值比较，
// 标识符列表较短者排名较低。
func (v Version) Compare(other Version) int {
	if order := cmp.Compare(v.Major, other.Major); order != 0 {
		return order
	}
	if order := cmp.Compare(v.Minor, other.Minor); order != 0 {
		return order
	}
	if order := cmp.Compare(v.Patch, other.Patch); order != 0 {
		return order
	}
	switch {
	case len(v.Pre) == 0 && len(other.Pre) == 0:
		return 0
	case len(v.Pre) == 0:
		return 1
	case len(other.Pre) == 0:
		return -1
	}
	for index := 0; index < len(v.Pre) && index < len(other.Pre); index++ {
		if order := compareIdentifier(v.Pre[index], other.Pre[index]); order != 0 {
			return order
		}
	}
	return cmp.Compare(len(v.Pre), len(other.Pre))
}

// isNumericIdentifier 报告 identifier 是否完全由 ASCII
// 数字组成。这类标识符按数值而非字典序比较。
func isNumericIdentifier(identifier string) bool {
	if identifier == "" {
		return false
	}
	for index := 0; index < len(identifier); index++ {
		if identifier[index] < '0' || identifier[index] > '9' {
			return false
		}
	}
	return true
}

func compareIdentifier(left, right string) int {
	leftNumber := isNumericIdentifier(left)
	rightNumber := isNumericIdentifier(right)
	switch {
	case leftNumber && rightNumber:
		return compareNumericIdentifier(left, right)
	case leftNumber:
		// 数字标识符的优先级始终较低。
		return -1
	case rightNumber:
		return 1
	default:
		return strings.Compare(left, right)
	}
}

// compareNumericIdentifier 按值对数字标识符排序，不经过
// int，因此宽度超过机器字长的标识符也能正确比较。
// 前导零（解析时无效，但手工构造的 Version 中可能出现）
// 会先被去掉。
func compareNumericIdentifier(left, right string) int {
	left = strings.TrimLeft(left, "0")
	right = strings.TrimLeft(right, "0")
	if left == "" {
		left = "0"
	}
	if right == "" {
		right = "0"
	}
	if order := cmp.Compare(len(left), len(right)); order != 0 {
		return order
	}
	return strings.Compare(left, right)
}

// String 渲染不带 v 前缀和构建元数据的版本。
func (v Version) String() string {
	text := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if len(v.Pre) > 0 {
		text += "-" + strings.Join(v.Pre, ".")
	}
	return text
}

// CompareVersions 比较两个版本字符串。无法解析的字符串被当作
// 0.0.0，这样本地版本损坏时仍能看到已发布的版本。
func CompareVersions(left, right string) int {
	first, err := ParseVersion(left)
	if err != nil {
		first = Version{}
	}
	second, err := ParseVersion(right)
	if err != nil {
		second = Version{}
	}
	return first.Compare(second)
}
