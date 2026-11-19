// Package update checks GitHub releases for a newer argent build, downloads the
// right asset with a checksum, and replaces the running binary atomically.
//
// Only the standard library is used: release metadata comes from
// /releases/latest, assets are plain HTTP downloads, and the swap is a rename.
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

// apiBase is a variable so tests (and GitHub Enterprise installs) can point the
// client elsewhere.
var apiBase = "https://api.github.com"

// Asset is one downloadable release file.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

// Release is the subset of the GitHub release payload argent uses.
type Release struct {
	Repo        string    `json:"-"`
	Tag         string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	URL         string    `json:"html_url"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []Asset   `json:"assets"`
}

// Version is the tag without its leading "v".
func (r Release) Version() string { return strings.TrimPrefix(r.Tag, "v") }

// Check fetches the latest release of repo ("owner/name") and reports whether it
// is newer than currentVersion. A nil client uses a 30-second default.
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

// Version is a semantic version. Build metadata is dropped at parse time, so
// v1.2.3+build.5 and v1.2.3 compare equal.
type Version struct {
	Major int
	Minor int
	Patch int
	// Pre holds the prerelease identifiers; empty for a final release.
	Pre []string
}

// ParseVersion accepts "1.2.3", "v1.2.3", "1.2", and prerelease suffixes such
// as "1.0.0-beta.2". Build metadata after "+" is ignored.
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

// Compare orders two versions by the semver precedence rules: a release is
// higher than any of its prereleases, numeric identifiers compare numerically,
// and a shorter identifier list sorts lower.
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

func compareIdentifier(left, right string) int {
	leftNumber, leftErr := strconv.Atoi(left)
	rightNumber, rightErr := strconv.Atoi(right)
	switch {
	case leftErr == nil && rightErr == nil:
		return cmp.Compare(leftNumber, rightNumber)
	case leftErr == nil:
		// Numeric identifiers always have lower precedence.
		return -1
	case rightErr == nil:
		return 1
	default:
		return strings.Compare(left, right)
	}
}

// String renders the version without the v prefix or build metadata.
func (v Version) String() string {
	text := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if len(v.Pre) > 0 {
		text += "-" + strings.Join(v.Pre, ".")
	}
	return text
}

// CompareVersions compares two version strings. An unparsable string is treated
// as 0.0.0 so a broken local version still sees published releases.
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
