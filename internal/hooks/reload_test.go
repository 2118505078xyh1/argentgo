package hooks

import (
	"errors"
	"testing"
)

// snapshotSource 是会计数读取次数的测试来源，用于验证单读与不改入参。
type snapshotSource struct {
	key   string
	data  []byte
	err   error
	reads int
}

func (s *snapshotSource) Key() string { return s.key }

func (s *snapshotSource) Bytes() ([]byte, error) {
	s.reads++
	if s.err != nil {
		return nil, s.err
	}
	return s.data, nil
}

func newSnapshotSource(key string, data []byte, err error) *snapshotSource {
	return &snapshotSource{key: key, data: data, err: err}
}

// isEmptySnapshot 报告快照是否为零值（无变化 / 失败时的返回形态）。
func isEmptySnapshot(s ConfigSnapshot) bool {
	return s.Config == nil && s.Fingerprint == "" && s.Generation == 0
}

// secondTrustedDocument 是第二个合法钩子文档，用于多源合并。
const secondTrustedDocument = `{"hooks":{"PostToolUse":[{"command":"echo second"}]}}`

func TestSnapshotFromSources(t *testing.T) {
	docA := []byte(trustedDocument)
	docB := []byte(secondTrustedDocument)

	// 预先算出几个基准指纹，供"无变化"与精确指纹断言使用。
	_, fpA, err := fingerprintSources([]Source{newSnapshotSource("hooks/a.json", docA, nil)})
	if err != nil {
		t.Fatalf("fingerprintSources(a): %v", err)
	}
	_, fpAB, err := fingerprintSources([]Source{
		newSnapshotSource("hooks/a.json", docA, nil),
		newSnapshotSource("hooks/b.json", docB, nil),
	})
	if err != nil {
		t.Fatalf("fingerprintSources(a,b): %v", err)
	}

	tests := []struct {
		name        string
		sources     []*snapshotSource
		trusted     map[string]bool // key -> 是否授予其内容摘要
		nilTrust    bool
		prevFP      string
		wantChanged bool
		wantErrIs   error
		wantParse   bool
		wantCmds    int
		wantFirst   string
		wantFP      string
		wantReads   []int // 每个来源期望的 Bytes 调用次数；nil 表示不检查
	}{
		{
			name:      "内容无变化返回空快照",
			sources:   []*snapshotSource{newSnapshotSource("hooks/a.json", docA, nil)},
			trusted:   map[string]bool{"hooks/a.json": true},
			prevFP:    fpA,
			wantReads: []int{1},
		},
		{
			name:        "授信源采纳",
			sources:     []*snapshotSource{newSnapshotSource("hooks/a.json", docA, nil)},
			trusted:     map[string]bool{"hooks/a.json": true},
			prevFP:      "",
			wantChanged: true,
			wantCmds:    1,
			wantFirst:   "echo checked",
			wantFP:      fpA,
			wantReads:   []int{1},
		},
		{
			name:      "未授信拒绝",
			sources:   []*snapshotSource{newSnapshotSource("hooks/a.json", docA, nil)},
			trusted:   nil, // 不授予任何摘要
			prevFP:    "",
			wantErrIs: ErrUntrusted,
		},
		{
			name:      "nil 信任存储拒绝",
			sources:   []*snapshotSource{newSnapshotSource("hooks/a.json", docA, nil)},
			nilTrust:  true,
			prevFP:    "",
			wantErrIs: ErrUntrusted,
		},
		{
			name:      "读失败",
			sources:   []*snapshotSource{newSnapshotSource("hooks/a.json", nil, errSourceRead)},
			trusted:   map[string]bool{"hooks/a.json": true},
			prevFP:    "",
			wantErrIs: errSourceRead,
		},
		{
			name:      "解析失败",
			sources:   []*snapshotSource{newSnapshotSource("hooks/a.json", []byte("not-json"), nil)},
			trusted:   map[string]bool{"hooks/a.json": true},
			prevFP:    "",
			wantParse: true,
		},
		{
			name:      "信任门先于解析",
			sources:   []*snapshotSource{newSnapshotSource("hooks/a.json", []byte("{bad json"), nil)},
			trusted:   nil,
			prevFP:    "",
			wantErrIs: ErrUntrusted,
		},
		{
			name: "多源合并按信任键顺序",
			// 故意逆序传入：合并顺序应由排序后的键决定，与输入顺序无关。
			sources: []*snapshotSource{
				newSnapshotSource("hooks/b.json", docB, nil),
				newSnapshotSource("hooks/a.json", docA, nil),
			},
			trusted: map[string]bool{
				"hooks/a.json": true,
				"hooks/b.json": true,
			},
			prevFP:      "",
			wantChanged: true,
			wantCmds:    2,
			wantFirst:   "echo checked",
			wantFP:      fpAB,
		},
		{
			name:        "CJK Key 与分隔符归一化",
			sources:     []*snapshotSource{newSnapshotSource("配置/钩子.json", docA, nil)},
			trusted:     map[string]bool{`配置\钩子.json`: true},
			prevFP:      "",
			wantChanged: true,
			wantCmds:    1,
			wantFirst:   "echo checked",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NewTrustStore()
			if !tt.nilTrust {
				// 授予键可能与来源键仅分隔符不同（CJK 用例），故按归一化路径匹配。
				for grantKey := range tt.trusted {
					for _, src := range tt.sources {
						if normalizePath(grantKey) == normalizePath(src.key) {
							store.Trust(grantKey, Digest(src.data))
							break
						}
					}
				}
			}
			var trust *TrustStore = store
			if tt.nilTrust {
				trust = nil
			}
			sources := make([]Source, 0, len(tt.sources))
			for _, src := range tt.sources {
				sources = append(sources, src)
			}

			snap, changed, err := SnapshotFromSources(sources, trust, tt.prevFP)

			if tt.wantErrIs != nil || tt.wantParse {
				if err == nil {
					t.Fatal("expected an error")
				}
				if tt.wantErrIs != nil && !errors.Is(err, tt.wantErrIs) {
					t.Fatalf("error = %v, want errors.Is(_, %v)", err, tt.wantErrIs)
				}
				if tt.wantParse && errors.Is(err, ErrUntrusted) {
					t.Fatalf("trusted bytes should reach the parser, got ErrUntrusted: %v", err)
				}
				if changed {
					t.Fatal("changed must be false on error")
				}
				if !isEmptySnapshot(snap) {
					t.Fatalf("snapshot should be empty on error, got %+v", snap)
				}
				return
			}
			if err != nil {
				t.Fatalf("SnapshotFromSources: %v", err)
			}
			if changed != tt.wantChanged {
				t.Fatalf("changed = %v, want %v", changed, tt.wantChanged)
			}
			if !tt.wantChanged {
				if !isEmptySnapshot(snap) {
					t.Fatalf("unchanged snapshot should be empty, got %+v", snap)
				}
			} else {
				if isEmptySnapshot(snap) || snap.Config == nil {
					t.Fatalf("expected a populated snapshot, got %+v", snap)
				}
				if len(snap.Config.Commands) != tt.wantCmds {
					t.Fatalf("commands = %d, want %d", len(snap.Config.Commands), tt.wantCmds)
				}
				if tt.wantFirst != "" && snap.Config.Commands[0].Command != tt.wantFirst {
					t.Fatalf("first command = %q, want %q", snap.Config.Commands[0].Command, tt.wantFirst)
				}
				if snap.Generation != 1 {
					t.Fatalf("Generation = %d, want 1", snap.Generation)
				}
				if tt.wantFP != "" && snap.Fingerprint != tt.wantFP {
					t.Fatalf("Fingerprint = %q, want %q", snap.Fingerprint, tt.wantFP)
				}
			}
			if tt.wantReads != nil {
				if len(tt.wantReads) != len(tt.sources) {
					t.Fatalf("wantReads has %d entries, sources have %d", len(tt.wantReads), len(tt.sources))
				}
				for i, src := range tt.sources {
					if src.reads != tt.wantReads[i] {
						t.Fatalf("source %d reads = %d, want %d", i, src.reads, tt.wantReads[i])
					}
				}
			}
		})
	}
}

func TestSnapshotFromSourcesFingerprintStable(t *testing.T) {
	docA := []byte(trustedDocument)
	docB := []byte(secondTrustedDocument)

	first := []Source{
		newSnapshotSource("hooks/a.json", docA, nil),
		newSnapshotSource("hooks/b.json", docB, nil),
	}
	reversed := []Source{
		newSnapshotSource("hooks/b.json", docB, nil),
		newSnapshotSource("hooks/a.json", docA, nil),
	}

	_, fp1, err := fingerprintSources(first)
	if err != nil {
		t.Fatalf("fingerprintSources(first): %v", err)
	}
	_, fp2, err := fingerprintSources(reversed)
	if err != nil {
		t.Fatalf("fingerprintSources(reversed): %v", err)
	}
	if fp1 == "" || fp1 != fp2 {
		t.Fatalf("fingerprint not stable across input order: %q vs %q", fp1, fp2)
	}

	// 用 prevFP 走一遍无变化路径：逆序输入也应被识别为同一内容。
	_, changed, err := SnapshotFromSources(reversed, NewTrustStore(), fp1)
	if err != nil || changed {
		t.Fatalf("same content in different order: changed=%v err=%v", changed, err)
	}

	// 内容或键任一变化，指纹都应改变。
	_, fpChanged, err := fingerprintSources([]Source{
		newSnapshotSource("hooks/a.json", []byte(trustedDocument+" "), nil),
		newSnapshotSource("hooks/b.json", docB, nil),
	})
	if err != nil {
		t.Fatalf("fingerprintSources(changed): %v", err)
	}
	if fpChanged == fp1 {
		t.Fatal("changing content must change the fingerprint")
	}
	_, fpNewKey, err := fingerprintSources([]Source{
		newSnapshotSource("hooks/z.json", docA, nil),
		newSnapshotSource("hooks/b.json", docB, nil),
	})
	if err != nil {
		t.Fatalf("fingerprintSources(new-key): %v", err)
	}
	if fpNewKey == fp1 {
		t.Fatal("changing a key must change the fingerprint")
	}
}

func TestSnapshotFromSourcesDoesNotMutateInput(t *testing.T) {
	docA := []byte(trustedDocument)
	docB := []byte(secondTrustedDocument)
	a := newSnapshotSource("hooks/a.json", docA, nil)
	b := newSnapshotSource("hooks/b.json", docB, nil)
	sources := []Source{b, a} // 逆序，且有重复元素检验顺序不被改写

	store := NewTrustStore()
	store.Trust(a.key, Digest(a.data))
	store.Trust(b.key, Digest(b.data))

	if _, changed, err := SnapshotFromSources(sources, store, ""); err != nil || !changed {
		t.Fatalf("SnapshotFromSources: changed=%v err=%v", changed, err)
	}

	// 入参切片的顺序与内容必须原样保留。
	if sources[0] != Source(b) || sources[1] != Source(a) {
		t.Fatal("sources slice order was mutated")
	}
	if a.key != "hooks/a.json" || b.key != "hooks/b.json" {
		t.Fatal("source keys were mutated")
	}
	if string(a.data) != trustedDocument || string(b.data) != secondTrustedDocument {
		t.Fatal("source bytes were mutated")
	}
	// 求指纹时只读一次；加载阶段复用同一份字节，不再二次读取。
	if a.reads != 1 || b.reads != 1 {
		t.Fatalf("reads = (%d, %d), want (1, 1)", a.reads, b.reads)
	}
}

func TestSnapshotFromSourcesEmptySources(t *testing.T) {
	_, fp, err := fingerprintSources(nil)
	if err != nil {
		t.Fatalf("fingerprintSources(nil): %v", err)
	}
	if fp == "" {
		t.Fatal("empty source set should still produce a stable fingerprint")
	}

	snap, changed, err := SnapshotFromSources(nil, NewTrustStore(), "")
	if err != nil || !changed {
		t.Fatalf("first poll: changed=%v err=%v", changed, err)
	}
	if snap.Config == nil || len(snap.Config.Commands) != 0 {
		t.Fatalf("empty config expected, got %+v", snap.Config)
	}
	if snap.Fingerprint != fp {
		t.Fatalf("Fingerprint = %q, want %q", snap.Fingerprint, fp)
	}

	snap, changed, err = SnapshotFromSources(nil, NewTrustStore(), fp)
	if err != nil || changed || !isEmptySnapshot(snap) {
		t.Fatalf("second poll: changed=%v empty=%v err=%v", changed, isEmptySnapshot(snap), err)
	}
}
