package skills

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseVirtualURI(t *testing.T) {
	valid := []struct {
		name      string
		raw       string
		wantName  string
		wantPath  string
		canonical string
	}{
		{"standard", "embed://deploy/SKILL.md", "deploy", "SKILL.md", "embed://deploy/SKILL.md"},
		{"nested path", "embed://tools/references/api.md", "tools", "references/api.md", "embed://tools/references/api.md"},
		{"surrounding space", "  embed://deploy/SKILL.md  ", "deploy", "SKILL.md", "embed://deploy/SKILL.md"},
		{"uppercase scheme", "EMBED://deploy/SKILL.md", "deploy", "SKILL.md", "embed://deploy/SKILL.md"},
	}
	for _, tc := range valid {
		t.Run("valid/"+tc.name, func(t *testing.T) {
			uri, err := ParseVirtualURI(tc.raw)
			if err != nil {
				t.Fatalf("ParseVirtualURI(%q): %v", tc.raw, err)
			}
			if uri.Scheme != VirtualScheme || uri.Name != tc.wantName || uri.Path != tc.wantPath {
				t.Errorf("uri = %#v, want scheme=%q name=%q path=%q", uri, VirtualScheme, tc.wantName, tc.wantPath)
			}
			if got := uri.String(); got != tc.canonical {
				t.Errorf("String() = %q, want %q", got, tc.canonical)
			}
			// 规范形式必须能被再次解析，形成闭环。
			if _, err := ParseVirtualURI(uri.String()); err != nil {
				t.Errorf("canonical URI does not round-trip: %v", err)
			}
		})
	}

	invalid := []struct {
		name string
		raw  string
	}{
		{"empty", ""},
		{"no scheme", "deploy/SKILL.md"},
		{"wrong scheme", "http://deploy/SKILL.md"},
		{"no double slash", "embed:deploy/SKILL.md"},
		{"no name", "embed:///SKILL.md"},
		{"no path", "embed://deploy"},
		{"trailing slash", "embed://deploy/"},
		{"query", "embed://deploy/SKILL.md?x=1"},
		{"fragment", "embed://deploy/SKILL.md#part"},
		{"userinfo", "embed://user@deploy/SKILL.md"},
		{"port", "embed://deploy:80/SKILL.md"},
		{"dot dot segment", "embed://deploy/../SKILL.md"},
		{"empty middle segment", "embed://deploy/a//SKILL.md"},
		{"bad escape", "embed://deploy/%zz/SKILL.md"},
	}
	for _, tc := range invalid {
		t.Run("invalid/"+tc.name, func(t *testing.T) {
			if _, err := ParseVirtualURI(tc.raw); !errors.Is(err, ErrInvalidVirtualURI) {
				t.Errorf("ParseVirtualURI(%q) error = %v, want ErrInvalidVirtualURI", tc.raw, err)
			}
		})
	}
}

// 虚拟加载与磁盘发现调用同一个 Parse，并对产出的 Skill 一视同仁：
// 除了来源路径本身，所有字段必须逐字节一致。
func TestVirtualAndDiskSharePipeline(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "deploy")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	diskPath := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(diskPath, []byte(sample), 0o644); err != nil {
		t.Fatal(err)
	}

	diskSkill, ok := Discover([]string{root}).Get("deploy")
	if !ok {
		t.Fatal("disk skill missing")
	}

	const uri = "embed://deploy/SKILL.md"
	source := MapVirtualSource{uri: sample}
	virtualRegistry, err := DiscoverVirtual(source, []string{uri})
	if err != nil {
		t.Fatalf("DiscoverVirtual: %v", err)
	}
	virtualSkill, ok := virtualRegistry.Get("deploy")
	if !ok {
		t.Fatal("virtual skill missing")
	}
	if virtualSkill.Path != uri {
		t.Errorf("virtual Path = %q, want %q", virtualSkill.Path, uri)
	}

	// Path 是来源差异，其余字段必须完全相同。
	diskSkill.Path, virtualSkill.Path = "", ""
	if !reflect.DeepEqual(diskSkill, virtualSkill) {
		t.Errorf("disk and virtual skills differ:\ndisk    = %#v\nvirtual = %#v", diskSkill, virtualSkill)
	}
}

// 显式列出的虚拟来源一旦找不到即报错，并保留可判别的哨兵错误。
func TestDiscoverVirtualNotFound(t *testing.T) {
	cases := []struct {
		name    string
		uris    []string
		source  VirtualSource
		wantErr error
	}{
		{"missing document", []string{"embed://ghost/SKILL.md"}, MapVirtualSource{}, ErrNotFound},
		{"malformed uri", []string{"not-a-uri"}, MapVirtualSource{}, ErrInvalidVirtualURI},
		{"nil source", []string{"embed://deploy/SKILL.md"}, nil, ErrNotFound},
		{
			"source error",
			[]string{"embed://deploy/SKILL.md"},
			VirtualFunc(func(VirtualURI) (string, error) { return "", fmt.Errorf("backend: %w", ErrNotFound) }),
			ErrNotFound,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg, err := DiscoverVirtual(tc.source, tc.uris)
			if reg != nil {
				t.Errorf("registry = %#v, want nil on error", reg)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("error = %v, want errors.Is(_, %v)", err, tc.wantErr)
			}
		})
	}
}

// 虚拟技能可以合并进磁盘发现得到的注册表，沿用同样的先到先得语义。
func TestAddVirtualMergesIntoDiskRegistry(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "deploy", "disk deploy")
	registry := Discover([]string{root})

	const uri = "embed://review/SKILL.md"
	text := "---\nname: review\ndescription: embedded review\n---\nbody of review"
	if _, err := registry.AddVirtual(MapVirtualSource{uri: text}, uri); err != nil {
		t.Fatalf("AddVirtual: %v", err)
	}
	// 同名虚拟技能不得覆盖已存在的磁盘技能。
	if _, err := registry.AddVirtual(MapVirtualSource{uri: sample}, uri); err != nil {
		t.Fatalf("AddVirtual: %v", err)
	}
	if registry.Len() != 2 {
		t.Fatalf("len = %d, want 2", registry.Len())
	}
	deploy, _ := registry.Get("deploy")
	if !strings.Contains(deploy.Description, "disk") {
		t.Errorf("disk skill was overwritten: %#v", deploy)
	}
	if _, ok := registry.Get("review"); !ok {
		t.Error("embedded skill missing from registry")
	}
}

// LoadVirtual 的可选清单：不传时走原加载路径；一旦传入，任何条目校验失败都拒绝加载。
func TestLoadVirtualWithManifest(t *testing.T) {
	const (
		uri    = "embed://deploy/SKILL.md"
		second = "embed://review/SKILL.md"
		// ParseVirtualURI 的 name/path 段只接受 ASCII，CJK URI 会在解析这一步失败。
		cjkURI = "embed://部署/指南.md"
	)
	data := []byte(sample)
	other := []byte("---\nname: review\ndescription: review doc\n---\nbody of review")
	source := MapVirtualSource{uri: sample, second: string(other)}

	shaBad := entryFor(uri, data)
	shaBad.SHA256 = strings.Repeat("0", 64) // 等长但摘要不符，只触发 SHA 不符

	sizeBad := entryFor(uri, data)
	sizeBad.Size++ // 摘要不变，仅长度不符

	resolveBad := entryFor("embed://ghost/SKILL.md", []byte("x")) // source 中没有该 URI
	cjkBad := AssetEntry{URI: cjkURI, SHA256: digest(other), Size: int64(len(other))}

	cases := []struct {
		name      string
		source    VirtualSource
		raw       string
		manifests []AssetManifest
		wantName  string // 非空表示期望加载成功并得到该技能名
		wantErr   error
	}{
		{
			name:     "no manifest keeps original behavior",
			source:   source,
			raw:      uri,
			wantName: "deploy",
		},
		{
			name:      "single matching manifest passes",
			source:    source,
			raw:       uri,
			manifests: []AssetManifest{{Entries: []AssetEntry{entryFor(uri, data)}}},
			wantName:  "deploy",
		},
		{
			name:   "multiple manifests merged and pass",
			source: source,
			raw:    uri,
			manifests: []AssetManifest{
				{Entries: []AssetEntry{entryFor(uri, data)}},
				{Entries: []AssetEntry{entryFor(second, other)}},
			},
			wantName: "deploy",
		},
		{
			name:      "sha mismatch rejects",
			source:    source,
			raw:       uri,
			manifests: []AssetManifest{{Entries: []AssetEntry{shaBad}}},
			wantErr:   ErrAssetHashMismatch,
		},
		{
			name:      "size mismatch rejects",
			source:    source,
			raw:       uri,
			manifests: []AssetManifest{{Entries: []AssetEntry{sizeBad}}},
			wantErr:   ErrAssetSizeMismatch,
		},
		{
			name:      "resolve failure rejects",
			source:    source,
			raw:       uri,
			manifests: []AssetManifest{{Entries: []AssetEntry{resolveBad}}},
			wantErr:   ErrAssetResolve,
		},
		{
			// CJK URI 不能通过 ParseVirtualURI，校验按解析失败拒绝，不得静默放行。
			name:      "cjk uri rejected at parse step",
			source:    source,
			raw:       uri,
			manifests: []AssetManifest{{Entries: []AssetEntry{cjkBad}}},
			wantErr:   ErrAssetResolve,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			skill, err := LoadVirtual(tc.source, tc.raw, tc.manifests...)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error = %v, want errors.Is(_, %v)", err, tc.wantErr)
				}
				if skill.Name != "" {
					t.Errorf("skill = %#v, want zero Skill on rejection", skill)
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadVirtual: %v", err)
			}
			if skill.Name != tc.wantName {
				t.Errorf("skill.Name = %q, want %q", skill.Name, tc.wantName)
			}
		})
	}
}

// DiscoverVirtual 的可选清单：校验发生在注册之前，任何条目不符即整体失败，不做部分注册。
func TestDiscoverVirtualWithManifest(t *testing.T) {
	const (
		a      = "embed://a/SKILL.md"
		b      = "embed://b/SKILL.md"
		cjkURI = "embed://部署/指南.md"
	)
	docA := "---\nname: a\ndescription: skill a\n---\nbody a"
	docB := "---\nname: b\ndescription: skill b\n---\nbody b"
	source := MapVirtualSource{a: docA, b: docB}
	dataA := []byte(docA)
	dataB := []byte(docB)

	sizeBad := entryFor(a, dataA)
	sizeBad.Size++

	// 与 a 的合法记录使用不同 URI，避免被 MergeManifests 的去重丢弃。
	sizeBadB := entryFor(b, dataB)
	sizeBadB.Size++

	resolveBad := entryFor("embed://ghost/SKILL.md", []byte("x"))
	cjkBad := AssetEntry{URI: cjkURI, SHA256: digest(dataB), Size: int64(len(dataB))}

	cases := []struct {
		name      string
		uris      []string
		manifests []AssetManifest
		wantNames []string
		wantErr   error
	}{
		{
			name:      "no manifest keeps original behavior",
			uris:      []string{b, a},
			wantNames: []string{"a", "b"}, // 与 Discover 一致按名称排序
		},
		{
			name:      "all entries pass",
			uris:      []string{a, b},
			manifests: []AssetManifest{{Entries: []AssetEntry{entryFor(a, dataA), entryFor(b, dataB)}}},
			wantNames: []string{"a", "b"},
		},
		{
			name:      "single mismatch rejects whole registry",
			uris:      []string{a, b},
			manifests: []AssetManifest{{Entries: []AssetEntry{entryFor(a, dataA), sizeBadB}}},
			wantErr:   ErrAssetSizeMismatch,
		},
		{
			name:      "resolve failure rejects",
			uris:      []string{a},
			manifests: []AssetManifest{{Entries: []AssetEntry{resolveBad}}},
			wantErr:   ErrAssetResolve,
		},
		{
			// CJK URI 不能通过 ParseVirtualURI，校验按解析失败拒绝。
			name:      "cjk uri rejected at parse step",
			uris:      []string{a},
			manifests: []AssetManifest{{Entries: []AssetEntry{cjkBad}}},
			wantErr:   ErrAssetResolve,
		},
		{
			// 校验与 URI 列表无关：即便没有要加载的技能，坏清单也必须让调用失败。
			name:      "empty uri list still verifies manifest",
			uris:      nil,
			manifests: []AssetManifest{{Entries: []AssetEntry{sizeBad}}},
			wantErr:   ErrAssetSizeMismatch,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg, err := DiscoverVirtual(source, tc.uris, tc.manifests...)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error = %v, want errors.Is(_, %v)", err, tc.wantErr)
				}
				if reg != nil {
					t.Errorf("registry = %#v, want nil on rejection", reg)
				}
				return
			}
			if err != nil {
				t.Fatalf("DiscoverVirtual: %v", err)
			}
			var got []string
			for _, s := range reg.skills {
				got = append(got, s.Name)
			}
			if !reflect.DeepEqual(got, tc.wantNames) {
				t.Errorf("names = %v, want %v", got, tc.wantNames)
			}
		})
	}
}
