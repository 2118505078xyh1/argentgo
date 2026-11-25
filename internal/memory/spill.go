package memory

// 大条目落盘（受控溢出）是 Bank 的可选能力，借鉴工具输出产出的溢出机制：
// 超过阈值的文本写入独立文件，主记录只保留有界预览与一个恢复 URI，全文
// 可通过 Recover 按需取回。默认关闭，此时所有记录与日志字节都和本功能
// 加入之前完全一致，也不会创建或扫描任何额外目录。

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"example.com/argentgo/internal/security"
)

const (
	// spillDirName 是 bank 目录下存放大条目全文的子目录。
	spillDirName = "spill"
	// spillSuffix 是落盘文件的扩展名。
	spillSuffix = ".txt"
	// spillURIScheme 是恢复 URI 的协议前缀；URI 形如 "memspill:<id>"。
	spillURIScheme = "memspill:"

	// DefaultSpillThreshold 是默认落盘阈值（字节）。取 maxTextBytes，
	// 恰好覆盖旧实现会因 maxTextBytes 上限而截断、从而丢失的正文。
	DefaultSpillThreshold = maxTextBytes
	// DefaultSpillPreview 是默认内联预览字节数。
	DefaultSpillPreview = 256
)

// ErrMemoryNotFound 在 Recover 找不到指定 id 时返回。
var ErrMemoryNotFound = errors.New("memory: id not found")

// SpillOptions 描述大条目落盘策略。零值字段回退到默认值。
type SpillOptions struct {
	// Threshold 是触发落盘的字节数：脱敏后的全文严格大于该值时落盘。
	// <=0 时使用 DefaultSpillThreshold。
	Threshold int
	// Preview 是内联预览保留的字节数。<=0 时使用 DefaultSpillPreview。
	Preview int
}

// threshold 返回生效的落盘阈值。
func (o SpillOptions) threshold() int {
	if o.Threshold > 0 {
		return o.Threshold
	}
	return DefaultSpillThreshold
}

// preview 返回生效的预览字节数。
func (o SpillOptions) preview() int {
	if o.Preview > 0 {
		return o.Preview
	}
	return DefaultSpillPreview
}

// SetSpill 启用或关闭大条目落盘。传 nil 关闭（这也是零值 Bank 的默认行为）。
// 与 SetDir 一样，应在并发使用之前调用。
func (b *Bank) SetSpill(opts *SpillOptions) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if opts == nil {
		b.spill = nil
		return
	}
	clone := *opts
	b.spill = &clone
}

// SpillEnabled 报告是否已启用大条目落盘。
func (b *Bank) SpillEnabled() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.spill != nil
}

// maybeSpill 在启用落盘且文本超过阈值时，把脱敏后的全文写入独立文件，
// 并把 mem 改写为有界预览加恢复 URI。调用方持有 b.mu。
// 写盘失败时保持 mem 不变（退化为普通的内联存储），因此落盘绝不会让
// 一次成功的存储变成错误，也不会留下悬空的 URI。
func (b *Bank) maybeSpill(mem *Memory, text string) {
	if b.spill == nil {
		return
	}
	full := spillBody(text)
	if len(full) <= b.spill.threshold() {
		return
	}
	if err := writeSpillFile(b.spillPath(mem.ID), []byte(full)); err != nil {
		return
	}
	mem.Text = boundedPreview(full, b.spill.preview())
	mem.SpillURI = spillURI(mem.ID)
	mem.SpillBytes = len(full)
}

// spillBody 生成落盘全文：脱敏并裁掉首尾空白，但不施加 maxTextBytes
// 上限，从而完整保留那些原本会被截断的内容。
func spillBody(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return strings.TrimSpace(security.Redact(s))
}

// boundedPreview 从已脱敏的全文取前缀预览，最多 limit 字节且落在
// UTF-8 rune 边界上，因此中文和 emoji 不会被切坏。
func boundedPreview(full string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(full) > limit {
		full = clipRunes(full, limit)
	}
	return strings.TrimSpace(full)
}

// spillURI 生成 id 对应的恢复 URI。
func spillURI(id string) string { return spillURIScheme + id }

// spillFileName 是落盘文件的命名契约：<安全 id>.txt。
func spillFileName(id string) string { return id + spillSuffix }

// isSpillFileName 判断文件名是否符合本包的落盘命名约定。
func isSpillFileName(name string) bool {
	if !strings.HasSuffix(name, spillSuffix) {
		return false
	}
	return safeSpillID(strings.TrimSuffix(name, spillSuffix))
}

// spillPath 返回 id 对应的落盘文件绝对路径。id 由 newID 生成，只含
// 字母、数字与 '-'，因此不含路径分隔符。
func (b *Bank) spillPath(id string) string {
	return filepath.Join(b.dir, spillDirName, spillFileName(id))
}

// spillFilePath 把恢复 URI 解析为受管理目录内的路径。只接受本包生成的
// "memspill:<id>" 形式，任何额外路径成分、分隔符或非法字符都会被拒绝，
// 避免读取或删除 spill 目录之外的文件。
func (b *Bank) spillFilePath(uri string) (string, bool) {
	if !strings.HasPrefix(uri, spillURIScheme) {
		return "", false
	}
	id := strings.TrimPrefix(uri, spillURIScheme)
	if !safeSpillID(id) {
		return "", false
	}
	return b.spillPath(id), true
}

// safeSpillID 只允许字母、数字、'-' 与 '_'，从而杜绝路径分隔符和 ".."。
func safeSpillID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

// writeSpillFile 以独占方式创建落盘文件；写入或关闭失败时移除可能产生
// 的不完整文件，绝不留下半个产物。
func writeSpillFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

// Recover 取回 id 对应记录的全文。未落盘的记录直接返回内联 Text；
// 已落盘的记录从恢复 URI 指向的落盘文件读回完整内容。未知 id 返回
// ErrMemoryNotFound；落盘文件缺失或 URI 非法时返回相应错误。
func (b *Bank) Recover(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", errors.New("memory: id must not be empty")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	all, err := b.loadLocked()
	if err != nil {
		return "", err
	}
	var target *Memory
	for i := range all {
		if all[i].ID == id {
			target = &all[i]
			break
		}
	}
	if target == nil {
		return "", ErrMemoryNotFound
	}
	if target.SpillURI == "" {
		return target.Text, nil
	}
	path, ok := b.spillFilePath(target.SpillURI)
	if !ok {
		return "", fmt.Errorf("memory: invalid spill uri %q", target.SpillURI)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("memory: recover %s: %w", id, err)
	}
	return string(data), nil
}

// removeSpillFiles 尽力删除给定记录关联的落盘文件。路径由恢复 URI 解析
// 且被约束在受管理目录内，因此恶意 URI 无法越界删除。
func (b *Bank) removeSpillFiles(mems []Memory) {
	for _, mem := range mems {
		if mem.SpillURI == "" {
			continue
		}
		path, ok := b.spillFilePath(mem.SpillURI)
		if !ok {
			continue
		}
		_ = os.Remove(path)
	}
}

// CleanSpill 扫描受管理的落盘目录，删除不被任何记录引用的孤立文件，
// 返回删除数量。它只处理符合本包命名约定的文件，不会触碰其他内容。
func (b *Bank) CleanSpill() (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	dir := filepath.Join(b.dir, spillDirName)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("memory: spill read dir: %w", err)
	}
	all, err := b.loadLocked()
	if err != nil {
		return 0, err
	}
	referenced := make(map[string]bool, len(all))
	for _, mem := range all {
		if path, ok := b.spillFilePath(mem.SpillURI); ok {
			referenced[path] = true
		}
	}
	removed := 0
	for _, e := range entries {
		if e.IsDir() || !isSpillFileName(e.Name()) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if referenced[path] {
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return removed, fmt.Errorf("memory: spill remove: %w", err)
		}
		removed++
	}
	return removed, nil
}
