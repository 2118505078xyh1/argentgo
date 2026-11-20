package server

import (
	"runtime"
	"runtime/debug"
	"strings"
)

const (
	unknownBuildMeta = "unknown"
	defaultVersion   = "dev"
)

// Commit and BuildID are optional link-time build metadata, injected with e.g.
//
//	go build -ldflags "-X github.com/2118505078xyh1/argentgo/internal/server.Commit=abc123 -X github.com/2118505078xyh1/argentgo/internal/server.BuildID=build-42"
//
// Both default to "unknown" so an uninjected build still returns a complete,
// stable JSON object. When Commit is not injected, the Go toolchain's recorded
// VCS revision is used as a best-effort fallback.
var (
	Commit  = unknownBuildMeta
	BuildID = unknownBuildMeta
)

// VersionInfo is the structured build report returned by GET /v1/version.
// Every field is always present, so clients can rely on the schema even in a
// dev build that carries no injected metadata.
type VersionInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildID   string `json:"build_id"`
	GoVersion string `json:"go_version"`
	Platform  string `json:"platform"`
}

// newVersionInfo collects the report from the configured version plus the
// running binary's toolchain and target. It performs no I/O and reads no
// config, so the endpoint stays fast and deterministic across calls.
func newVersionInfo(version string) VersionInfo {
	version = strings.TrimSpace(version)
	if version == "" {
		version = defaultVersion
	}
	buildID := strings.TrimSpace(BuildID)
	if buildID == "" {
		buildID = unknownBuildMeta
	}
	return VersionInfo{
		Version:   version,
		Commit:    buildCommit(),
		BuildID:   buildID,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
}

// buildCommit prefers an explicitly injected Commit; otherwise it falls back
// to the VCS revision that the Go toolchain records in the binary. Missing
// metadata is reported honestly as "unknown" rather than guessed.
func buildCommit() string {
	if commit := strings.TrimSpace(Commit); commit != "" && commit != unknownBuildMeta {
		return commit
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return unknownBuildMeta
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			if revision := strings.TrimSpace(setting.Value); revision != "" {
				return revision
			}
		}
	}
	return unknownBuildMeta
}
