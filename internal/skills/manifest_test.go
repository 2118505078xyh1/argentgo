package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// digest 计算测试内容的十六进制 SHA-256 摘要。
func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// entryFor 为一段内容构造一条完全匹配的清单记录。
func entryFor(uri string, data []byte) AssetEntry {
	return AssetEntry{URI: uri, SHA256: digest(data), Size: int64(len(data))}
}

// mapResolver 把 URI→内容 的映射适配成 resolve 函数，缺失的 URI 视为解析失败。
func mapResolver(m map[string][]byte) func(string) ([]byte, error) {
	return func(uri string) ([]byte, error) {
		b, ok := m[uri]
		if !ok {
			return nil, fmt.Errorf("asset not found: %s", uri)
		}
		return b, nil
	}
}

func TestVerifyManifest(t *testing.T) {
	const (
		uriA = "embed://deploy/SKILL.md"
		uriB = "embed://部署/指南.md" // CJK URI 也应被原样解析与比较
	)
	dataA := []byte("asset A content")
	dataB := []byte("资产 B 内容")
	// 与 dataA 等长但内容不同：只触发 SHA 不符，不触发 Size 不符。
	dataATampered := []byte("asset A contenT")
	// 短内容：与 dataA 的摘要/长度都不同。
	short := []byte("short")

	// 摘要不符 + 长度不符，但预期 URI 只出现一次。
	bothBad := entryFor(uriA, dataA)
	bothBad.Size++

	sizeBad := entryFor(uriA, dataA)
	sizeBad.Size += 10

	upperA := entryFor(uriA, dataA)
	upperA.SHA256 = strings.ToUpper(upperA.SHA256)

	cases := []struct {
		name     string
		manifest AssetManifest
		resolve  func(string) ([]byte, error)
		wantURIs []string
		wantErrs []error
	}{
		{
			name:     "all match",
			manifest: AssetManifest{Entries: []AssetEntry{entryFor(uriA, dataA), entryFor(uriB, dataB)}},
			resolve:  mapResolver(map[string][]byte{uriA: dataA, uriB: dataB}),
			wantURIs: nil,
			wantErrs: nil,
		},
		{
			name:     "uppercase digest accepted",
			manifest: AssetManifest{Entries: []AssetEntry{upperA}},
			resolve:  mapResolver(map[string][]byte{uriA: dataA}),
			wantURIs: nil,
			wantErrs: nil,
		},
		{
			name:     "single sha mismatch",
			manifest: AssetManifest{Entries: []AssetEntry{entryFor(uriA, dataA), entryFor(uriB, dataB)}},
			resolve:  mapResolver(map[string][]byte{uriA: dataATampered, uriB: dataB}),
			wantURIs: []string{uriA},
			wantErrs: []error{ErrAssetHashMismatch},
		},
		{
			name:     "size mismatch",
			manifest: AssetManifest{Entries: []AssetEntry{sizeBad}},
			resolve:  mapResolver(map[string][]byte{uriA: dataA}),
			wantURIs: []string{uriA},
			wantErrs: []error{ErrAssetSizeMismatch},
		},
		{
			name:     "resolve failure",
			manifest: AssetManifest{Entries: []AssetEntry{entryFor(uriA, dataA), entryFor(uriB, dataB)}},
			resolve:  mapResolver(map[string][]byte{uriB: dataB}), // 缺少 uriA
			wantURIs: []string{uriA},
			wantErrs: []error{ErrAssetResolve},
		},
		{
			name:     "nil resolve",
			manifest: AssetManifest{Entries: []AssetEntry{entryFor(uriA, dataA)}},
			resolve:  nil,
			wantURIs: []string{uriA},
			wantErrs: []error{ErrAssetResolve},
		},
		{
			name:     "both hash and size mismatch reported once",
			manifest: AssetManifest{Entries: []AssetEntry{bothBad}},
			resolve:  mapResolver(map[string][]byte{uriA: short}),
			wantURIs: []string{uriA},
			wantErrs: []error{ErrAssetHashMismatch, ErrAssetSizeMismatch},
		},
		{
			name:     "multiple failures keep manifest order",
			manifest: AssetManifest{Entries: []AssetEntry{entryFor(uriA, dataA), entryFor(uriB, dataB)}},
			// uriA 摘要与长度都不符，uriB 解析失败（缺少映射）。
			resolve:  mapResolver(map[string][]byte{uriA: short}),
			wantURIs: []string{uriA, uriB},
			wantErrs: []error{ErrAssetHashMismatch, ErrAssetSizeMismatch, ErrAssetResolve},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := VerifyManifest(tc.manifest, tc.resolve)
			if !reflect.DeepEqual(got, tc.wantURIs) {
				t.Errorf("mismatched URIs = %v, want %v", got, tc.wantURIs)
			}
			for _, want := range tc.wantErrs {
				if !errors.Is(err, want) {
					t.Errorf("error = %v, want errors.Is(_, %v)", err, want)
				}
			}
			if len(tc.wantErrs) == 0 && err != nil {
				t.Errorf("error = %v, want nil", err)
			}
		})
	}
}

// VerifyManifest 是纯函数：不得改动传入的清单（含其底层数组）。
func TestVerifyManifestDoesNotModifyInput(t *testing.T) {
	data := []byte("payload")
	e := entryFor("embed://keep/SKILL.md", data)
	entries := []AssetEntry{e}
	m := AssetManifest{Entries: entries}

	// 故意让校验失败，确保所有分支都不写回入参。
	if _, err := VerifyManifest(m, func(string) ([]byte, error) { return []byte("tampered"), nil }); err == nil {
		t.Fatal("expected verification error")
	}
	want := []AssetEntry{e}
	if !reflect.DeepEqual(m.Entries, want) || !reflect.DeepEqual(entries, want) {
		t.Fatalf("manifest was modified: m.Entries = %v, entries = %v, want %v", m.Entries, entries, want)
	}
}

func TestMergeManifests(t *testing.T) {
	const (
		a = "embed://a/SKILL.md"
		b = "embed://b/SKILL.md"
		c = "embed://c/SKILL.md"
		D = "embed://文档/指南.md" // CJK URI
	)

	cases := []struct {
		name string
		a    AssetManifest
		b    AssetManifest
		want []AssetEntry
	}{
		{
			name: "empty inputs",
			a:    AssetManifest{},
			b:    AssetManifest{},
			want: []AssetEntry{},
		},
		{
			name: "dedup keeps first and sorts by URI",
			a:    AssetManifest{Entries: []AssetEntry{{URI: b, SHA256: "from-a"}, {URI: a, SHA256: "from-a"}}},
			b:    AssetManifest{Entries: []AssetEntry{{URI: a, SHA256: "from-b"}, {URI: c, SHA256: "from-b"}}},
			want: []AssetEntry{{URI: a, SHA256: "from-a"}, {URI: b, SHA256: "from-a"}, {URI: c, SHA256: "from-b"}},
		},
		{
			name: "stable sort from single manifest",
			a:    AssetManifest{},
			b:    AssetManifest{Entries: []AssetEntry{{URI: c}, {URI: a}, {URI: b}}},
			want: []AssetEntry{{URI: a}, {URI: b}, {URI: c}},
		},
		{
			// Go 按 UTF-8 字节序比较字符串，故 ASCII 的 a/b 排在 CJK 的 D 之前。
			name: "cjk uris dedup and sort",
			a:    AssetManifest{Entries: []AssetEntry{{URI: D, SHA256: "doc"}, {URI: a}}},
			b:    AssetManifest{Entries: []AssetEntry{{URI: b}, {URI: D, SHA256: "ignored"}}},
			want: []AssetEntry{{URI: a}, {URI: b}, {URI: D, SHA256: "doc"}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := MergeManifests(tc.a, tc.b)
			if !reflect.DeepEqual(got.Entries, tc.want) {
				t.Errorf("Entries = %v, want %v", got.Entries, tc.want)
			}
		})
	}
}

// MergeManifests 返回独立副本：改动结果不会影响任一入参，改动入参也不会影响结果。
func TestMergeManifestsReturnsCopy(t *testing.T) {
	a := AssetManifest{Entries: []AssetEntry{{URI: "z", SHA256: "z-hash"}, {URI: "a", SHA256: "a-hash"}}}
	b := AssetManifest{Entries: []AssetEntry{{URI: "m", SHA256: "m-hash"}}}
	origA := AssetManifest{Entries: []AssetEntry{{URI: "z", SHA256: "z-hash"}, {URI: "a", SHA256: "a-hash"}}}
	origB := AssetManifest{Entries: []AssetEntry{{URI: "m", SHA256: "m-hash"}}}

	merged := MergeManifests(a, b)
	want := []AssetEntry{{URI: "a", SHA256: "a-hash"}, {URI: "m", SHA256: "m-hash"}, {URI: "z", SHA256: "z-hash"}}
	if !reflect.DeepEqual(merged.Entries, want) {
		t.Fatalf("merged entries = %v, want %v", merged.Entries, want)
	}

	// 改结果，入参必须不变。
	merged.Entries[0].SHA256 = "mutated"
	if !reflect.DeepEqual(a, origA) || !reflect.DeepEqual(b, origB) {
		t.Fatalf("mutating result changed inputs: a = %v, b = %v", a, b)
	}

	// 改入参，结果必须不变。
	a.Entries[0].SHA256 = "mutated-input"
	if merged.Entries[2].SHA256 != "z-hash" {
		t.Fatalf("mutating input changed result: merged = %v", merged.Entries)
	}
}
