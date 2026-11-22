package security

// 审计日志的按大小轮转。轮转默认关闭：它会改变文件形态（产生 .1/.2/... 归档），
// 因此必须由调用方显式开启。脱敏发生在 Record 的 marshal 之前，轮转只搬动已经
// 落盘的文件，不接触内容，所以不会破坏脱敏结果。

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
)

const (
	// defaultAuditMaxBytes 是未显式指定阈值时的默认轮转阈值（10 MiB）。
	defaultAuditMaxBytes int64 = 10 << 20
	// defaultAuditKeep 是未显式指定时保留的归档数量（不含当前文件）。
	defaultAuditKeep = 3
)

// AuditRotationConfig 描述审计日志的按大小轮转策略。
// MaxBytes <= 0 时取 10 MiB；Keep <= 0 时取 3。
// 该策略默认关闭，通过 SetAuditRotation（全局）或 AuditLog.SetRotation（单实例）开启。
type AuditRotationConfig struct {
	MaxBytes int64
	Keep     int
}

// defaultAuditRotation 是进程级默认轮转配置；nil 表示关闭（默认）。
var defaultAuditRotation atomic.Pointer[AuditRotationConfig]

// SetAuditRotation 开启（或更新）进程级审计日志轮转。轮转默认关闭，调用此函数即 opt-in。
// 已在运行中的 AuditLog 没有单独配置时，下一次 Record 就会采用新的全局配置。
func SetAuditRotation(cfg AuditRotationConfig) {
	defaultAuditRotation.Store(&cfg)
}

// DisableAuditRotation 关闭进程级轮转配置，恢复默认（无轮转）行为。
// 已通过 AuditLog.SetRotation 单独开启的实例不受影响。
func DisableAuditRotation() {
	defaultAuditRotation.Store(nil)
}

// SetRotation 为单个 AuditLog 开启（或更新）轮转，覆盖进程级默认配置。
func (l *AuditLog) SetRotation(cfg AuditRotationConfig) {
	l.rotation.Store(&cfg)
}

// ClearRotation 清除本实例的轮转配置，使其回落到进程级默认（默认关闭）。
func (l *AuditLog) ClearRotation() {
	l.rotation.Store(nil)
}

func init() {
	// 运维侧可以不改代码，仅通过环境变量 opt-in。解析失败一律忽略（尽力而为）。
	enabled := false
	switch strings.ToLower(os.Getenv("ARGENT_AUDIT_ROTATE")) {
	case "1", "true", "yes", "on":
		enabled = true
	}
	if !enabled {
		return
	}
	cfg := AuditRotationConfig{MaxBytes: defaultAuditMaxBytes, Keep: defaultAuditKeep}
	if v := os.Getenv("ARGENT_AUDIT_ROTATE_MAX_BYTES"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			cfg.MaxBytes = n
		}
	}
	if v := os.Getenv("ARGENT_AUDIT_ROTATE_KEEP"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Keep = n
		}
	}
	defaultAuditRotation.Store(&cfg)
}

func normalizeRotation(cfg AuditRotationConfig) (maxBytes int64, keep int) {
	maxBytes, keep = cfg.MaxBytes, cfg.Keep
	if maxBytes <= 0 {
		maxBytes = defaultAuditMaxBytes
	}
	if keep <= 0 {
		keep = defaultAuditKeep
	}
	return maxBytes, keep
}

func archivePath(path string, i int) string {
	return fmt.Sprintf("%s.%d", path, i)
}

// maybeRotate 在文件大小超过阈值时尝试轮转。返回的错误由调用方忽略：
// 轮转失败时原文件保持可写，Record 会照常把新记录追加进去。
func (l *AuditLog) maybeRotate(path string, cfg AuditRotationConfig) error {
	maxBytes, keep := normalizeRotation(cfg)
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if info.Size() <= maxBytes {
		return nil
	}
	if l.rotate != nil {
		return l.rotate(path, cfg)
	}
	return rotateAuditFile(path, maxBytes, keep)
}

// rotateAuditFile 执行实际的按大小轮转：
//
//	<base>       -> <base>.1
//	<base>.1     -> <base>.2
//	...
//	<base>.(N-1) -> <base>.N
//
// 超过 N 的旧归档被删除。编号越大表示越旧；<base> 始终是最新数据。
// 操作步骤按"先删最旧、再由高到低平移、最后才动当前文件"的顺序进行，
// 这样在最后一步之前失败时当前文件完好，调用方回退原文件写入即可，不丢审计。
func rotateAuditFile(path string, maxBytes int64, keep int) error {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if info.Size() <= maxBytes {
		return nil
	}

	// 删除超出保留期限的最旧归档（崩溃残留的越界文件也一并清理）。
	// 这些数据的丢弃是 keep 策略本身允许的，不属于轮转失败造成的数据丢失。
	for i := keep + 1; ; i++ {
		p := archivePath(path, i)
		if _, err := os.Stat(p); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				break
			}
			return err
		}
		if err := os.Remove(p); err != nil {
			return err
		}
	}

	// 由高到低平移，避免低编号先把高编号覆盖掉。中间可能缺档，缺档即跳过。
	for i := keep - 1; i >= 1; i-- {
		err := replaceRename(archivePath(path, i), archivePath(path, i+1))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
	}

	// 最后一步才把当前文件落到 .1：此前任何一步失败，当前文件都原封不动。
	return replaceRename(path, archivePath(path, 1))
}

// replaceRename 把 src 改名为 dst。若 dst 已存在，先把它暂存到 .bak 再做替换，
// 替换失败时回滚，保证任一步异常都不会让两端数据同时消失。
// Windows 的 os.Rename 不允许覆盖已存在的目标，因此不能简单 rename。
func replaceRename(src, dst string) error {
	if _, err := os.Stat(src); err != nil {
		return err
	}
	if _, err := os.Stat(dst); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return os.Rename(src, dst)
	}

	backup := dst + ".bak"
	// 清理上次崩溃可能遗留的备份（它已被本轮更完整的数据取代）。
	if err := os.Remove(backup); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(dst, backup); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err != nil {
		_ = os.Rename(backup, dst) // 回滚
		return err
	}
	// 备份删除失败只会残留一个 .bak 文件，数据已经安全落在 dst。
	_ = os.Remove(backup)
	return nil
}
