package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// AssetManifest 描述一组资产及其期望的内容摘要与长度。
// 它是纯数据：VerifyManifest 校验它但绝不改动它，MergeManifests 合并它但不复用它的底层数组。
type AssetManifest struct {
	Entries []AssetEntry
}

// AssetEntry 是清单中的一条资产记录。
// URI 是资产的来源地址（例如 embed://name/SKILL.md），SHA256 是内容的十六进制摘要，
// Size 是内容的字节数。
type AssetEntry struct {
	URI    string
	SHA256 string
	Size   int64
}

// 资产校验失败的哨兵错误，调用方可用 errors.Is 判别具体原因。
var (
	// ErrAssetResolve 表示解析资产内容这一步本身失败（取内容的过程出错）。
	ErrAssetResolve = errors.New("asset resolve failed")
	// ErrAssetHashMismatch 表示资产内容的 SHA-256 与清单不符。
	ErrAssetHashMismatch = errors.New("asset SHA-256 mismatch")
	// ErrAssetSizeMismatch 表示资产内容的字节数与清单不符。
	ErrAssetSizeMismatch = errors.New("asset size mismatch")
)

// VerifyManifest 逐条解析 m 中的资产并校验 SHA-256 与 Size。
// 它返回校验失败的 URI 列表（每条失败记录在列表中占一项，顺序与 m.Entries 一致），
// 全部通过时返回 nil 列表与 nil 错误。失败时错误用上面的哨兵错误包装，
// 多条失败通过 errors.Join 合并，调用方可对返回的 error 使用 errors.Is。
// 同一条记录即使摘要与长度都不符，URI 也只出现一次。
// 本函数是纯函数：既不修改 m，也不持有 resolve 返回的切片。
func VerifyManifest(m AssetManifest, resolve func(uri string) ([]byte, error)) ([]string, error) {
	if resolve == nil {
		resolve = func(string) ([]byte, error) { return nil, errors.New("nil resolve function") }
	}

	var mismatched []string
	var errs []error
	for _, e := range m.Entries {
		data, err := resolve(e.URI)
		if err != nil {
			mismatched = append(mismatched, e.URI)
			errs = append(errs, fmt.Errorf("%w: %s: %v", ErrAssetResolve, e.URI, err))
			continue
		}

		bad := false
		sum := sha256.Sum256(data)
		got := hex.EncodeToString(sum[:])
		// 清单里的十六进制摘要允许大小写混写，统一按不区分大小写比较。
		if !strings.EqualFold(got, e.SHA256) {
			bad = true
			errs = append(errs, fmt.Errorf("%w: %s: have %s want %s", ErrAssetHashMismatch, e.URI, got, e.SHA256))
		}
		if int64(len(data)) != e.Size {
			bad = true
			errs = append(errs, fmt.Errorf("%w: %s: have %d want %d", ErrAssetSizeMismatch, e.URI, len(data), e.Size))
		}
		if bad {
			mismatched = append(mismatched, e.URI)
		}
	}
	if len(errs) > 0 {
		return mismatched, errors.Join(errs...)
	}
	return mismatched, nil
}

// MergeManifests 把 a 与 b 合并成一份新清单：按 URI 去重（先出现者优先，
// 即 a 覆盖 b 中的同 URI 记录）、按 URI 稳定排序，并返回不共享底层数组的副本。
// 两个参数都是只读的，不会被修改。
func MergeManifests(a, b AssetManifest) AssetManifest {
	seen := make(map[string]struct{}, len(a.Entries)+len(b.Entries))
	entries := make([]AssetEntry, 0, len(a.Entries)+len(b.Entries))
	add := func(list []AssetEntry) {
		for _, e := range list {
			if _, ok := seen[e.URI]; ok {
				continue
			}
			seen[e.URI] = struct{}{}
			entries = append(entries, e)
		}
	}
	add(a.Entries)
	add(b.Entries)
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].URI < entries[j].URI })
	return AssetManifest{Entries: entries}
}
