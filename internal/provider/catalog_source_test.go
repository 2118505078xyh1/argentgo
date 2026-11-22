package provider

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testCatalogSource() CatalogSource {
	return CatalogSource{
		Version: catalogSourceVersion,
		Models: []Model{
			{ID: "gpt-x", Name: "GPT X"},
			{ID: "claude-y", Name: "Claude Y"},
		},
	}
}

// TestValidateCatalogSource 覆盖目录源的信任边界：版本、空表、空 ID、空名称
// 与重复 ID 都必须被拒绝，且错误可用哨兵区分。
func TestValidateCatalogSource(t *testing.T) {
	cases := []struct {
		name string
		src  CatalogSource
		want error
	}{
		{"valid", testCatalogSource(), nil},
		{"wrong version", CatalogSource{Version: 2, Models: testCatalogSource().Models}, ErrCatalogSourceFormat},
		{"empty list", CatalogSource{Version: catalogSourceVersion}, ErrCatalogSourceInvalid},
		{"empty id", CatalogSource{Version: catalogSourceVersion, Models: []Model{{ID: "", Name: "x"}}}, ErrCatalogSourceInvalid},
		{"empty name", CatalogSource{Version: catalogSourceVersion, Models: []Model{{ID: "m", Name: ""}}}, ErrCatalogSourceInvalid},
		{
			"duplicate id",
			CatalogSource{Version: catalogSourceVersion, Models: []Model{{ID: "m", Name: "a"}, {ID: "m", Name: "b"}}},
			ErrCatalogSourceInvalid,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateCatalogSource(tc.src)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("ValidateCatalogSource: %v", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want errors.Is(_, %v)", err, tc.want)
			}
		})
	}
}

// TestMarshalCatalogSourceDeterministic 确认同一份数据的不同构造顺序产出相同
// 字节流，使目录文件可作为可复现的发布物对比。
func TestMarshalCatalogSourceDeterministic(t *testing.T) {
	a := testCatalogSource()
	b := testCatalogSource()
	b.Models[0], b.Models[1] = b.Models[1], b.Models[0]

	ba, err := MarshalCatalogSource(a)
	if err != nil {
		t.Fatalf("marshal a: %v", err)
	}
	bb, err := MarshalCatalogSource(b)
	if err != nil {
		t.Fatalf("marshal b: %v", err)
	}
	if string(ba) != string(bb) {
		t.Fatalf("order changed output:\na=%s\nb=%s", ba, bb)
	}
	if !strings.Contains(string(ba), "\"claude-y\"") {
		t.Fatalf("missing sorted model in %s", ba)
	}
	if a, b := SourceFingerprint(a), SourceFingerprint(b); a != b {
		t.Fatalf("fingerprints differ for reordered source: %q vs %q", a, b)
	}
}

// TestWriteCatalogAtomicRoundTrip 验证原子写的完整回路：写后能严格读回，
// 指纹一致，且临时文件被清理，只留下目标文件。
func TestWriteCatalogAtomicRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "catalog.json")
	src := testCatalogSource()

	if err := WriteCatalogAtomic(path, src); err != nil {
		t.Fatalf("WriteCatalogAtomic: %v", err)
	}
	got, err := LoadCatalogStrict(path)
	if err != nil {
		t.Fatalf("LoadCatalogStrict: %v", err)
	}
	if got.Version != src.Version || len(got.Models) != len(src.Models) {
		t.Fatalf("loaded = %+v, want %+v", got, src)
	}
	if a, b := SourceFingerprint(got), SourceFingerprint(src); a != b {
		t.Fatalf("fingerprint %q != %q", a, b)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "catalog.json" {
		t.Fatalf("leftover temp files: %+v", entries)
	}
}

// TestWriteCatalogAtomicRejectsInvalidWithoutTouchingTarget 确认无效源在写临时
// 文件之前就被拒绝，已有目标文件保持原样。这是"原子"在校验阶段的体现。
func TestWriteCatalogAtomicRejectsInvalidWithoutTouchingTarget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.json")
	original := "original bytes"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatalf("seed target: %v", err)
	}

	bad := CatalogSource{Version: catalogSourceVersion, Models: []Model{{ID: "dup"}, {ID: "dup"}}}
	if err := WriteCatalogAtomic(path, bad); !errors.Is(err, ErrCatalogSourceInvalid) {
		t.Fatalf("err = %v, want ErrCatalogSourceInvalid", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(data) != original {
		t.Fatalf("target changed to %q", data)
	}
}

// TestLoadCatalogStrictRejectsBadData 覆盖严格加载：未知字段、损坏 JSON、
// 尾随内容都不能被静默当成空目录或降级读取。
func TestLoadCatalogStrictRejectsBadData(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		data string
	}{
		{"unknown field", `{"version":1,"models":[{"ID":"m","Name":"n"}],"surprise":true}`},
		{"broken json", `{"version":1,`},
		{"trailing data", `{"version":1,"models":[{"ID":"m","Name":"n"}]} {}`},
		{"empty models", `{"version":1,"models":[]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "_")+".json")
			if err := os.WriteFile(path, []byte(tc.data), 0o600); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			if _, err := LoadCatalogStrict(path); !errors.Is(err, ErrCatalogSourceInvalid) && !errors.Is(err, ErrCatalogSourceFormat) {
				t.Fatalf("err = %v, want a rejection sentinel", err)
			}
		})
	}
}
