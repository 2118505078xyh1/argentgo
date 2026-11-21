package skills

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// VirtualScheme 是内置虚拟 skill 文档的 URI scheme。
// 约定形如 embed://deploy/SKILL.md：scheme 固定，name 是技能名，path 是文档在技能内的相对路径。
const VirtualScheme = "embed"

// ErrInvalidVirtualURI 表示虚拟 URI 的格式不合法（与"找不到"区分开）。
var ErrInvalidVirtualURI = errors.New("invalid virtual skill URI")

// VirtualURI 是一个虚拟 skill 文档的地址。
// 它把 embed://name/SKILL.md 拆成 scheme、name 与 path 三段，
// String 可无损还原为规范形式，供来源做精确键查找。
type VirtualURI struct {
	Scheme string
	Name   string
	Path   string // 不含前导斜杠，例如 SKILL.md 或 references/guide.md
}

// String 把 URI 还原为 embed://name/path 的规范形式。
// 空 Path 视为缺省的 SKILL.md，便于直接构造 VirtualURI 做查找。
func (u VirtualURI) String() string {
	path := u.Path
	if path == "" {
		path = "SKILL.md"
	}
	scheme := u.Scheme
	if scheme == "" {
		scheme = VirtualScheme
	}
	return scheme + "://" + u.Name + "/" + strings.TrimPrefix(path, "/")
}

// ParseVirtualURI 解析并严格校验一个虚拟 skill URI。
// 只接受 <scheme>://<name>/<path> 形式；scheme 必须等于 VirtualScheme，
// name 与 path 的每段都必须非空、不含路径穿越片段，且不允许 userinfo、端口、查询与片段。
func ParseVirtualURI(raw string) (VirtualURI, error) {
	trimmed := strings.TrimSpace(raw)
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return VirtualURI{}, fmt.Errorf("%w: %q: %v", ErrInvalidVirtualURI, raw, err)
	}
	if parsed.Scheme != VirtualScheme {
		return VirtualURI{}, fmt.Errorf("%w: %q (scheme must be %q)", ErrInvalidVirtualURI, raw, VirtualScheme)
	}
	if parsed.User != nil || parsed.Port() != "" {
		return VirtualURI{}, fmt.Errorf("%w: %q (userinfo and port are not allowed)", ErrInvalidVirtualURI, raw)
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return VirtualURI{}, fmt.Errorf("%w: %q (query and fragment are not allowed)", ErrInvalidVirtualURI, raw)
	}
	if !validVirtualSegment(parsed.Host) {
		return VirtualURI{}, fmt.Errorf("%w: %q (missing or invalid name)", ErrInvalidVirtualURI, raw)
	}

	path := strings.TrimPrefix(parsed.Path, "/")
	if path == "" {
		return VirtualURI{}, fmt.Errorf("%w: %q (missing document path)", ErrInvalidVirtualURI, raw)
	}
	segments := strings.Split(path, "/")
	for _, segment := range segments {
		if !validVirtualSegment(segment) {
			return VirtualURI{}, fmt.Errorf("%w: %q (invalid path segment %q)", ErrInvalidVirtualURI, raw, segment)
		}
	}
	return VirtualURI{Scheme: VirtualScheme, Name: parsed.Host, Path: path}, nil
}

// validVirtualSegment 报告 s 是否可作为 name 或 path 的一段。
// 只允许字母、数字、'.'、'_'、'-'；拒绝 '.' 与 '..' 以阻断路径穿越。
func validVirtualSegment(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

// VirtualSource 按虚拟 URI 提供文档正文。调用方注入内存实现（map、embed.FS 适配层等）。
// 找不到时实现应返回包装了 ErrNotFound 的错误。
type VirtualSource interface {
	Open(uri VirtualURI) (string, error)
}

// VirtualFunc 把函数适配为 VirtualSource。
type VirtualFunc func(VirtualURI) (string, error)

// Open 实现 VirtualSource。
func (f VirtualFunc) Open(uri VirtualURI) (string, error) { return f(uri) }

// MapVirtualSource 是最简单的内存来源：键为规范 URI（embed://name/path）或裸 name。
type MapVirtualSource map[string]string

// Open 实现 VirtualSource，先按规范 URI 查找，再按裸 name 查找。
func (m MapVirtualSource) Open(uri VirtualURI) (string, error) {
	if text, ok := m[uri.String()]; ok {
		return text, nil
	}
	if text, ok := m[uri.Name]; ok {
		return text, nil
	}
	return "", fmt.Errorf("%w: %s", ErrNotFound, uri)
}

// LoadVirtual 从 source 读取一个虚拟文档并送进与磁盘发现完全相同的 Parse 管线。
// 这里把虚拟 URI 当作 Parse 的 path 参数：诊断信息记录 URI，回退名称由 URI 中的 name 推导，
// 与磁盘路径 ".../name/SKILL.md" 得到相同的结果。
func LoadVirtual(source VirtualSource, raw string) (Skill, error) {
	uri, err := ParseVirtualURI(raw)
	if err != nil {
		return Skill{}, err
	}
	if source == nil {
		return Skill{}, fmt.Errorf("%w: nil source for %s", ErrNotFound, uri)
	}
	text, err := source.Open(uri)
	if err != nil {
		return Skill{}, fmt.Errorf("open virtual skill %s: %w", uri, err)
	}
	// 与 Discover 中对磁盘文件所做的调用逐字相同，确保解析行为一致。
	return Parse(text, uri.String())
}

// DiscoverVirtual 从给定的虚拟 URI 列表加载技能，构建与 Discover 同构的 Registry：
// 相同的 Parse、相同的 addIfAbsent（先到先得）以及相同的按名称排序。
// 与静默跳过坏文件的磁盘发现不同，显式列出的虚拟来源一旦出错即返回错误。
func DiscoverVirtual(source VirtualSource, uris []string) (*Registry, error) {
	r := New()
	for _, raw := range uris {
		skill, err := LoadVirtual(source, raw)
		if err != nil {
			return nil, err
		}
		r.addIfAbsent(skill)
	}
	sort.Slice(r.skills, func(i, j int) bool { return r.skills[i].Name < r.skills[j].Name })
	return r, nil
}

// AddVirtual 把单个虚拟技能加载并注册到已有 r，供把嵌入技能合并进磁盘发现的注册表。
// 注册语义与 Discover 中的 addIfAbsent 一致：同名技能不覆盖已存在者。
func (r *Registry) AddVirtual(source VirtualSource, raw string) (Skill, error) {
	skill, err := LoadVirtual(source, raw)
	if err != nil {
		return Skill{}, err
	}
	r.addIfAbsent(skill)
	return skill, nil
}
