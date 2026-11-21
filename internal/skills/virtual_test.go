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
