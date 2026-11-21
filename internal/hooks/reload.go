package hooks

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
)

// ConfigSnapshot 是钩子配置在某个时间点的只读快照。
//
// Config 是合并后的配置；Fingerprint 标识产生该配置的来源字节集合；
// Generation 是快照代数。它不含 I/O、定时器或 goroutine，由调用方自行
// 保存与传递。零值 Fingerprint 保留给"无新内容"的哨兵语义。
type ConfigSnapshot struct {
	Config      *Config
	Fingerprint string
	Generation  int
}

// sourceStamp 是单个来源在指纹计算中的表示：信任键、内容摘要，
// 以及读取时得到的字节（供加载阶段复用，避免二次读取）。
type sourceStamp struct {
	key    string
	digest string
	data   []byte
}

// fingerprintSources 读取每个来源的字节并计算聚合指纹，返回按信任键排序的
// 戳记与指纹字符串。
//
// 聚合方式：先对每个来源的字节取 SHA-256 摘要，再按信任键排序，最后把
// "键（以字节长度前缀界定）+ 摘要"依次写入 SHA-256。排序使指纹与传入
// 顺序无关；长度前缀使含分隔符或 CJK 字符的键也不会产生歧义。
//
// 任一来源读取失败立即返回错误。该函数只读来源，不改动 sources。
func fingerprintSources(sources []Source) ([]sourceStamp, string, error) {
	stamps := make([]sourceStamp, 0, len(sources))
	for _, src := range sources {
		data, err := src.Bytes()
		if err != nil {
			return nil, "", fmt.Errorf("hooks: read %s: %w", src.Key(), err)
		}
		stamps = append(stamps, sourceStamp{
			key:    src.Key(),
			digest: Digest(data),
			data:   data,
		})
	}

	// 摘要参与次级排序，使重复键的输入在不同顺序下仍得到同一指纹。
	sort.Slice(stamps, func(i, j int) bool {
		if stamps[i].key != stamps[j].key {
			return stamps[i].key < stamps[j].key
		}
		return stamps[i].digest < stamps[j].digest
	})

	h := sha256.New()
	for _, stamp := range stamps {
		// digest 是固定 64 字符的十六进制，键用字节长度前缀界定，
		// 因此拼接结果唯一可解析；CJK 键按原始 UTF-8 字节参与哈希。
		fmt.Fprintf(h, "%d:%s%s", len(stamp.key), stamp.key, stamp.digest)
	}
	return stamps, hex.EncodeToString(h.Sum(nil)), nil
}

// SnapshotFromSources 从一组来源构造钩子配置快照。
//
// 它先对来源字节求聚合指纹（见 fingerprintSources）：若与 prevFingerprint
// 相同则说明内容未变，返回零值快照与 changed=false，不触发任何加载。
// 内容变化时逐源调用 LoadSource 加载，信任门在解析之前；未授信、读失败或
// 解析失败都立即以 (零值, false, err) 返回，由调用方保留 last-good。全部
// 成功后按信任键顺序合并配置，返回新快照与 changed=true。
//
// 本函数是纯函数：除 Source.Bytes 外不做 I/O，不改动 sources 或 trust 的
// 状态。成功快照的 Generation 固定为 1（与 config 首次加载一致）；调用方
// 若在长生命周期中维护代数，应在采纳时用 lastGood.Generation+1 覆盖它。
func SnapshotFromSources(sources []Source, trust *TrustStore, prevFingerprint string) (ConfigSnapshot, bool, error) {
	stamps, fingerprint, err := fingerprintSources(sources)
	if err != nil {
		return ConfigSnapshot{}, false, err
	}
	if fingerprint == prevFingerprint {
		return ConfigSnapshot{}, false, nil
	}

	merged := &Config{}
	for _, stamp := range stamps {
		// 复用求指纹时读到的同一份字节：既避免二次读取，也保证被加载的
		// 内容与指纹标识的内容完全一致（不受来源后续变化影响）。
		data := stamp.data
		src := NewKeyedSource(stamp.key, SourceFunc(func() ([]byte, error) {
			return data, nil
		}))
		doc, err := LoadSource(src, trust)
		if err != nil {
			return ConfigSnapshot{}, false, err
		}
		merged.Merge(doc.Config)
	}

	return ConfigSnapshot{
		Config:      merged,
		Fingerprint: fingerprint,
		Generation:  1,
	}, true, nil
}
