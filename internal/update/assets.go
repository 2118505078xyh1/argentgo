package update

import (
	"runtime"
	"sort"
	"strings"
)

var osAliases = map[string][]string{
	"windows": {"windows", "win64", "win32", "win"},
	"darwin":  {"darwin", "macos", "osx", "mac"},
	"linux":   {"linux"},
	"freebsd": {"freebsd"},
}

var archAliases = map[string][]string{
	"amd64": {"amd64", "x86_64", "x86-64", "x64"},
	"arm64": {"arm64", "aarch64"},
	"386":   {"386", "i386", "x86"},
	"arm":   {"armv7", "armv6", "arm"},
}

// FindAsset picks the release asset for a platform. It understands the usual
// naming schemes: argent_1.2.3_windows_amd64.zip, argent-windows-x64.zip,
// argent_1.2.3_linux_arm64.tar.gz. An asset that names only the operating
// system is accepted when no architecture-specific file exists; an asset naming
// a different architecture never is.
func FindAsset(assets []Asset, goos, goarch string) (Asset, bool) {
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	goos = strings.ToLower(strings.TrimSpace(goos))
	goarch = strings.ToLower(strings.TrimSpace(goarch))
	if goos == "" || goarch == "" {
		return Asset{}, false
	}

	best := Asset{}
	bestScore := -1
	for _, asset := range assets {
		info := inspectAsset(asset, goos, goarch)
		if !info.eligible || info.score <= bestScore {
			continue
		}
		best, bestScore = asset, info.score
	}
	if bestScore < 0 {
		return Asset{}, false
	}
	return best, true
}

// assetInspection is the shared decision behind FindAsset and
// DiagnoseAssetMatch. Keeping both on this helper makes the diagnostic unable
// to drift from the asset that is actually selected.
type assetInspection struct {
	osMatched   bool
	foreignOS   bool
	archMatched bool
	foreignArch bool
	score       int
	eligible    bool
	reason      AssetMatchReason
}

// inspectAsset classifies one asset for an already-normalized target platform.
func inspectAsset(asset Asset, goos, goarch string) assetInspection {
	lower := strings.ToLower(asset.Name)
	if asset.URL == "" {
		return assetInspection{reason: AssetMatchReasonNoURL}
	}
	if isNoise(lower) {
		return assetInspection{reason: AssetMatchReasonNoise}
	}
	name := normalizeName(asset.Name)
	matchedOS, foreignOS := classify(name, goos, osAliases)
	if !matchedOS || foreignOS {
		return assetInspection{
			osMatched: matchedOS, foreignOS: foreignOS,
			reason: AssetMatchReasonWrongOS,
		}
	}
	matchedArch, foreignArch := classify(name, goarch, archAliases)
	if foreignArch {
		return assetInspection{
			osMatched: true, archMatched: matchedArch, foreignArch: true,
			reason: AssetMatchReasonWrongArch,
		}
	}
	score := 10 + extensionBonus(lower, goos)
	if matchedArch {
		score += 100
	}
	return assetInspection{
		osMatched: true, archMatched: matchedArch,
		score: score, eligible: true, reason: AssetMatchReasonEligible,
	}
}

// normalizeName lowercases a file name and turns separators into dots so that
// aliases match on token boundaries: "arg-x86_64.zip" -> ".arg.x86.64.zip."
func normalizeName(name string) string {
	var builder strings.Builder
	builder.WriteByte('.')
	for _, char := range strings.ToLower(name) {
		if char == '_' || char == '-' {
			builder.WriteByte('.')
			continue
		}
		builder.WriteRune(char)
	}
	builder.WriteByte('.')
	return builder.String()
}

// classify reports whether the name mentions the target platform and whether it
// mentions a different one of the same kind. The longest alias wins, so
// "x86_64" counts as amd64 and not as 386. A name that mentions two different
// platforms with equally long aliases is ambiguous and is always treated as
// foreign: guessing would make FindAsset depend on Go's random map iteration
// order and could install the wrong architecture.
func classify(name, target string, table map[string][]string) (matched, foreign bool) {
	if _, known := table[target]; !known {
		// An unrecognised target has no aliases in the table, but a name that
		// mentions a *known* platform still names a different one. Treating that
		// as "no platform named" would let FindAsset fall back to, say, an amd64
		// build for a riscv64 machine. An explicit mention of the target wins;
		// otherwise any known alias makes the asset foreign.
		if mentions(name, []string{target}) {
			return true, false
		}
		_, mentioned, ambiguous := longestAlias(name, table)
		if mentioned || ambiguous {
			return false, true
		}
		return false, false
	}
	key, matched, ambiguous := longestAlias(name, table)
	if ambiguous {
		return false, true
	}
	if !matched {
		return false, false
	}
	return key == target, key != target
}

// longestAlias finds the platform key whose alias matches name with the longest
// token boundary needle. Different keys are visited in sorted order so equal
// lengths are handled deliberately: when two *different* keys match needles of
// the same length, ambiguous is set and callers must reject the asset instead
// of letting the random map iteration decide which platform it names.
func longestAlias(name string, table map[string][]string) (key string, matched, ambiguous bool) {
	keys := make([]string, 0, len(table))
	for candidate := range table {
		keys = append(keys, candidate)
	}
	sort.Strings(keys)

	bestLength := -1
	for _, candidate := range keys {
		needles := make([]string, 0, len(table[candidate])+1)
		for _, alias := range table[candidate] {
			needles = append(needles, "."+separatorFree(alias)+".")
		}
		needles = append(needles, "."+separatorFree(candidate)+".")
		for _, needle := range needles {
			if len(needle) < bestLength || !strings.Contains(name, needle) {
				continue
			}
			if len(needle) > bestLength {
				bestLength = len(needle)
				key, matched, ambiguous = candidate, true, false
				continue
			}
			// An equal-length match for the same key is just an alternative
			// spelling (for example x86_64 and x86-64). A different key means
			// the asset name names two platforms and cannot be trusted.
			if candidate != key {
				ambiguous = true
			}
		}
	}
	if ambiguous {
		return "", false, true
	}
	return key, matched, false
}

func mentions(name string, aliases []string) bool {
	for _, alias := range aliases {
		if strings.Contains(name, "."+separatorFree(alias)+".") {
			return true
		}
	}
	return false
}

func separatorFree(alias string) string {
	return strings.NewReplacer("_", ".", "-", ".").Replace(strings.ToLower(alias))
}

// isNoise filters out checksum, signature and metadata files.
func isNoise(lower string) bool {
	for _, suffix := range []string{
		".sha256", ".sha256sum", ".sha512", ".sig", ".asc", ".json", ".txt",
		".pem", ".sbom", ".spdx", ".md",
	} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	for _, word := range []string{"checksum", "sha256sums", "source"} {
		if strings.Contains(lower, word) {
			return true
		}
	}
	return false
}

func extensionBonus(lower, goos string) int {
	if goos == "windows" {
		switch {
		case strings.HasSuffix(lower, ".zip"):
			return 3
		case strings.HasSuffix(lower, ".exe"):
			return 2
		case strings.HasSuffix(lower, ".msi"):
			return 1
		}
		return 0
	}
	switch {
	case strings.HasSuffix(lower, ".tar.gz"):
		return 3
	case strings.HasSuffix(lower, ".tgz"):
		return 2
	case strings.HasSuffix(lower, ".zip"):
		return 1
	}
	return 0
}
