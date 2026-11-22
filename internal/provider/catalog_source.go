// Package provider 模型目录的生成式源、原子落盘与严格加载。
//
// 现有 catalog_cache.go 只负责合并/指纹等纯策略，templates.go 是编译期静态表，
// 但两者都没有"把一个权威目录作为一个整体原子替换、并在加载期拒绝可疑数据"
// 的边界。本文件补上这条边界：声明式源 -> 校验 -> 临时文件 -> rename 提交，
// 以及与之对称的 strict 加载。它只依赖标准库，不碰网络与全局状态。
package provider

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// catalogSourceVersion 是本文件理解的目录源格式版本。版本化让未来的结构变更
// 能显式拒绝旧数据，而不是静默地把缺失字段当成零值。
const catalogSourceVersion = 1

// 校验失败的哨兵错误。调用方可用 errors.Is 区分"数据不可信"与"文件打不开"。
var (
	ErrCatalogSourceInvalid = errors.New("invalid catalog source")
	ErrCatalogSourceFormat  = errors.New("unsupported catalog source format")
)

// CatalogSource 是一份可生成的模型目录源。它不是运行时缓存：缓存的合并/新鲜度
// 语义仍由 catalog_cache.go 管理，这里只描述"权威数据长什么样"以及怎么安全地
// 把它写到磁盘、再原样读回来。
type CatalogSource struct {
	Version int     `json:"version"`
	Models  []Model `json:"models"`
	Comment string  `json:"comment,omitempty"`
}

// ValidateCatalogSource 校验一份目录源是否可作为权威数据提交。
//
// 规则刻意保守，因为目录会被多个进程共享：拒绝空 ID、重复 ID、空名称、
// 不支持的版本，以及模型列表为空。返回的每条错误都带具体位置，便于生成端
// 定位是哪一行数据出了问题，而不是只报"格式错误"。
func ValidateCatalogSource(source CatalogSource) error {
	if source.Version != catalogSourceVersion {
		return fmt.Errorf("%w: version %d, want %d", ErrCatalogSourceFormat, source.Version, catalogSourceVersion)
	}
	if len(source.Models) == 0 {
		return fmt.Errorf("%w: empty model list", ErrCatalogSourceInvalid)
	}
	seen := make(map[string]int, len(source.Models))
	for i, model := range source.Models {
		if model.ID == "" {
			return fmt.Errorf("%w: models[%d] has empty ID", ErrCatalogSourceInvalid, i)
		}
		if model.Name == "" {
			return fmt.Errorf("%w: models[%d] (%s) has empty name", ErrCatalogSourceInvalid, i, model.ID)
		}
		if first, dup := seen[model.ID]; dup {
			return fmt.Errorf("%w: models[%d] duplicates ID %q first seen at %d", ErrCatalogSourceInvalid, i, model.ID, first)
		}
		seen[model.ID] = i
	}
	return nil
}

// MarshalCatalogSource 把目录源编码为确定性的 JSON 字节。
//
// 模型按 ID 排序，使同一份数据无论以什么顺序构造都得到相同字节流；这让
// FingerprintStatic 之外的文件级校验（例如发布物对比）也稳定可复现。
func MarshalCatalogSource(source CatalogSource) ([]byte, error) {
	if err := ValidateCatalogSource(source); err != nil {
		return nil, err
	}
	ordered := CatalogSource{
		Version: source.Version,
		Comment: source.Comment,
		Models:  append([]Model(nil), source.Models...),
	}
	sort.Slice(ordered.Models, func(i, j int) bool {
		return ordered.Models[i].ID < ordered.Models[j].ID
	})
	data, err := json.MarshalIndent(ordered, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal catalog source: %w", err)
	}
	return append(data, '\n'), nil
}

// WriteCatalogAtomic 把目录源原子地写入 path。
//
// 先在目标同目录建临时文件并写满、Sync、关闭，再 rename 覆盖目标；任何一步
// 失败都会删除临时文件，且目标文件保持写入前的原样。这样读者永远只会看到
// 完整的旧版本或完整的新版本，不会读到写到一半的 JSON。
func WriteCatalogAtomic(path string, source CatalogSource) error {
	data, err := MarshalCatalogSource(source)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create catalog dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".catalog-*.tmp")
	if err != nil {
		return fmt.Errorf("create catalog temp file: %w", err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		_ = tmp.Close()
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write catalog temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync catalog temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close catalog temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("commit catalog file: %w", err)
	}
	committed = true
	return nil
}

// LoadCatalogStrict 严格读取一份目录源：未知字段、损坏 JSON、不支持的版本
// 和任何校验错误都返回 error，绝不降级为空目录。
//
// 目录是共享的权威数据，"读数失败就当没有模型"会让调用方在错误前提下继续
// 运行；宁可显式失败，也不静默吞掉可疑内容。
func LoadCatalogStrict(path string) (CatalogSource, error) {
	var source CatalogSource
	data, err := os.ReadFile(path)
	if err != nil {
		return source, fmt.Errorf("read catalog source: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&source); err != nil {
		return source, fmt.Errorf("%w: decode %s: %v", ErrCatalogSourceInvalid, path, err)
	}
	if decoder.Decode(&struct{}{}) == nil {
		return source, fmt.Errorf("%w: %s has trailing data", ErrCatalogSourceInvalid, path)
	}
	if err := ValidateCatalogSource(source); err != nil {
		return source, err
	}
	return source, nil
}

// SourceFingerprint 返回目录源的内容指纹，复用 catalog_cache.go 的稳定算法。
//
// FingerprintStatic 本身对模型顺序敏感，而 MarshalCatalogSource 会按 ID 排序；
// 若直接把源的顺序传进去，"写入前的源"与"从文件读回的源"会算出不同指纹，
// 进而让缓存误判为目录已变。这里先按同一规则规范化，保证指纹与文件字节一致。
func SourceFingerprint(source CatalogSource) string {
	ordered := append([]Model(nil), source.Models...)
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].ID < ordered[j].ID
	})
	return FingerprintStatic(ordered)
}
