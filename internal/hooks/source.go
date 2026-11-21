package hooks

import "fmt"

// Source 是钩子文档字节及其信任键的来源。
//
// Key 返回用于信任匹配的稳定标识（通常是文件路径或逻辑名），Bytes 返回当前
// 字节。把"标识"与"内容"拆成两个方法，是为了让信任门在解析之前就能对内容做
// 摘要并查表，同时让来源可以是文件、内存、网络快照等任意实现。
type Source interface {
	Key() string
	Bytes() ([]byte, error)
}

// SourceFunc 把无参函数适配成字节来源。它只实现 Bytes，Key 由组合它的类型
// （见 NewKeyedSource）提供；SourceFunc 本身绝不参与信任判定，也不会绕过信任门。
type SourceFunc func() ([]byte, error)

// Bytes 调用底层函数返回字节。
func (f SourceFunc) Bytes() ([]byte, error) {
	return f()
}

// keyedSource 把信任键与字节来源组合成完整的 Source。
type keyedSource struct {
	key string
	fn  SourceFunc
}

// Key 返回构造时给定的信任键。
func (s keyedSource) Key() string { return s.key }

// Bytes 委托给底层的 SourceFunc。
func (s keyedSource) Bytes() ([]byte, error) { return s.fn.Bytes() }

// NewKeyedSource 返回以 key 为信任键、字节由 fn 提供的 Source。
// fn 只是"读"这一步的载体；真正的信任判定发生在 LoadSource 中。
func NewKeyedSource(key string, fn SourceFunc) Source {
	return keyedSource{key: key, fn: fn}
}

// LoadSource 从任意 Source 加载钩子文档。
//
// 它严格遵循与 LoadDocument 相同的顺序：读（src.Bytes）→ 摘要（Digest）→
// 信任门（trust.IsTrusted(src.Key(), digest)）→ 解析（ParseConfig）。src.Key()
// 作为信任键，因此"内容来自何处"与"内容是否被授权"是两回事：即便字节来自
// SourceFunc，未被授予该键/摘要的文档也会在解析之前被拒绝，返回包装
// ErrUntrusted 的错误。
//
// 该函数是纯函数：不读写全局状态、不改动 Source 或 TrustStore 的状态（除
// IsTrusted 的只读查询外），也不安装任何调用方。
func LoadSource(src Source, trust *TrustStore) (*Document, error) {
	key := src.Key()
	data, err := src.Bytes()
	if err != nil {
		return nil, fmt.Errorf("hooks: read %s: %w", key, err)
	}
	digest := Digest(data)
	if trust == nil || !trust.IsTrusted(key, digest) {
		return nil, fmt.Errorf("%s: %w", key, ErrUntrusted)
	}
	config, err := ParseConfig(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", key, err)
	}
	return &Document{Path: key, Digest: digest, Config: config}, nil
}
