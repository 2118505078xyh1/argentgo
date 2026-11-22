package app

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/tools"
)

// 输出产物落盘在这里接线，这是唯一能同时看到已解析存储目录和工具注册表的位置。
// tools 包拥有实现机制（WithOutputSpill / SpillLargeOutput）；本文件只决定
// 某次运行是否选择启用，以及产物存放的位置。
const (
	// outputSpillEnableEnv 是唯一的简单启用开关。真值与仓库中其他功能开关一致
	// （"1"、"true"、"yes"、"on"）。未设置时，configureOutputSpill 原样返回 ctx，
	// 每个现有运行的工具输出都逐字节不变。
	outputSpillEnableEnv = "ARGENT_OUTPUT_SPILL"

	// 这两个名字对应 tools 包的 spillConfig 环境变量开关，使操作人员能在组装时
	// 覆盖目录或字节阈值。它们在这里保持为字面量，以免扩大 tools 的 API。
	outputSpillDirEnv       = "ARGENT_OUTPUT_SPILL_DIR"
	outputSpillThresholdEnv = "ARGENT_OUTPUT_SPILL_THRESHOLD"

	// spillArtifactDirName 是一个约定：产物位于已解析的存储目录下，而非工作区下，
	// 因此它们绝不会污染仓库，并由该安装的所有会话/运行共享。
	spillArtifactDirName = "artifacts"
)

// outputSpillArtifactDir 解析某次运行稳定的产物目录。真实运行中存储目录总是由 Resolve 设置；
// os.TempDir() 回退只是保护那些构建裸 Resolved 的直接调用方（例如测试），
// 以免把一个相对的 "artifacts" 目录写进当前工作区。
func outputSpillArtifactDir(storage string) string {
	base := strings.TrimSpace(storage)
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, spillArtifactDirName)
}

// outputSpillEnabled 报告启用开关是否为真值。
func outputSpillEnabled(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// configureOutputSpill 让某次运行的工具输出选择启用产物落盘。
//
// 默认（未启用）是空操作：ctx 原样返回，因此 tools 包看不到配置，回退到普通的 clipTo 路径——
// 输出字节与之前完全一致，也不会创建或扫描任何目录。
//
// 通过 ARGENT_OUTPUT_SPILL 启用时，返回的 ctx 带有 tools.WithOutputSpill，
// 使用稳定的产物目录（<storage>/artifacts）和保守的默认阈值。
// ARGENT_OUTPUT_SPILL_DIR 和 ARGENT_OUTPUT_SPILL_THRESHOLD 可以覆盖这两者。
// 一次性的、尽力而为的保留清理会移除早于 tools.DefaultSpillRetention 的产物；
// 其失败会被刻意忽略，使清理绝不会中断运行。
func configureOutputSpill(ctx context.Context, env func(string) string, storage string) context.Context {
	get := func(key string) string {
		if env == nil {
			return ""
		}
		return strings.TrimSpace(env(key))
	}
	if !outputSpillEnabled(get(outputSpillEnableEnv)) {
		return ctx
	}

	dir := get(outputSpillDirEnv)
	if dir == "" {
		dir = outputSpillArtifactDir(storage)
	}
	threshold := tools.DefaultOutputSpillThreshold
	if raw := get(outputSpillThresholdEnv); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			threshold = n
		}
	}

	_, _ = tools.CleanSpillArtifacts(dir, tools.DefaultSpillRetention)
	// 磁盘容量闸门：默认 512MiB，超限按 mtime 淘汰最旧产出。
	// ARGENT_OUTPUT_SPILL_MAX_BYTES=0 可禁用容量清理（仅保留按时间清理）。
	maxBytes := int64(512) << 20
	if raw := get("ARGENT_OUTPUT_SPILL_MAX_BYTES"); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
			maxBytes = n // 允许 0 显式禁用；非法值保留默认
		}
	}
	_, _, _ = tools.TrimSpillArtifacts(dir, maxBytes)
	return tools.WithOutputSpill(ctx, threshold, dir)
}
