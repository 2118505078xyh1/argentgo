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

// Commit 和 BuildID 是可选的链接期构建元数据，可通过例如
//
//	go build -ldflags "-X github.com/2118505078xyh1/argentgo/internal/server.Commit=abc123 -X github.com/2118505078xyh1/argentgo/internal/server.BuildID=build-42"
//
// 两者都默认为 "unknown"，使未注入的构建也能返回完整、
// 稳定的 JSON 对象。当 Commit 未注入时，使用 Go 工具链记录的
// VCS 版本作为尽力而为的回退。
var (
	Commit  = unknownBuildMeta
	BuildID = unknownBuildMeta
)

// VersionInfo 是 GET /v1/version 返回的结构化构建报告。
// 每个字段始终存在，因此即使在没有任何注入元数据的
// 开发构建中，客户端也能依赖该 schema。
type VersionInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildID   string `json:"build_id"`
	GoVersion string `json:"go_version"`
	Platform  string `json:"platform"`
}

// newVersionInfo 从配置的 version 加上正在运行的二进制的
// 工具链和目标收集报告。它不执行 I/O、不读取
// 配置，因此该端点保持快速且跨调用确定。
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

// buildCommit 优先使用显式注入的 Commit；否则回退
// 到 Go 工具链记录在二进制中的 VCS 版本。缺失的
// 元数据会如实报告为 "unknown"，而不是猜测。
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
