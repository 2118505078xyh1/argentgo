package app

import (
	"strings"
	"testing"
)

// TestStatusSnapshotZeroValue 锁定零值与空构造都输出 {}，且没有尾随换行。
func TestStatusSnapshotZeroValue(t *testing.T) {
	tests := []struct {
		name string
		got  []byte
	}{
		{name: "zero value", got: (StatusSnapshot{}).JSON()},
		{name: "constructor with nil maps", got: NewStatusSnapshot(nil, nil, nil).JSON()},
		{name: "constructor with empty maps", got: NewStatusSnapshot(
			map[string]string{}, map[string]string{}, map[string]string{},
		).JSON()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if string(tt.got) != "{}" {
				t.Errorf("JSON() = %q, want %q", tt.got, "{}")
			}
			if strings.HasSuffix(string(tt.got), "\n") {
				t.Errorf("JSON() has trailing newline: %q", tt.got)
			}
		})
	}
}

// TestStatusSnapshotSections 覆盖三个分段的独立出现与同时出现，锁定字段顺序与字节。
func TestStatusSnapshotSections(t *testing.T) {
	tests := []struct {
		name string
		in   StatusSnapshot
		want string
	}{
		{
			name: "turn only",
			in:   NewStatusSnapshot(map[string]string{"phase": "streaming"}, nil, nil),
			want: `{"turn":{"values":{"phase":"streaming"}}}`,
		},
		{
			name: "readiness only",
			in:   NewStatusSnapshot(nil, map[string]string{"ready": "true"}, nil),
			want: `{"readiness":{"values":{"ready":"true"}}}`,
		},
		{
			name: "integrity only",
			in:   NewStatusSnapshot(nil, nil, map[string]string{"entry": "$.model"}),
			want: `{"integrity":{"values":{"entry":"$.model"}}}`,
		},
		{
			name: "all sections",
			in: NewStatusSnapshot(
				map[string]string{"phase": "idle"},
				map[string]string{"ready": "false"},
				map[string]string{"entry": "$.cwd"},
			),
			want: `{"turn":{"values":{"phase":"idle"}},"readiness":{"values":{"ready":"false"}},"integrity":{"values":{"entry":"$.cwd"}}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(tt.in.JSON())
			if got != tt.want {
				t.Errorf("JSON() = %s, want %s", got, tt.want)
			}
		})
	}
}

// TestStatusSnapshotRedactsSecrets 确认键和值中的凭据形状片段都被 security.Redact 替换。
func TestStatusSnapshotRedactsSecrets(t *testing.T) {
	secret := "sk-proj-abcdefghijklmnopqrstuvwxyz"
	snap := NewStatusSnapshot(
		map[string]string{"phase": secret},
		map[string]string{secret: "value"},
		map[string]string{"reason": "token=" + secret},
	)
	got := string(snap.JSON())

	if strings.Contains(got, secret) {
		t.Errorf("JSON() leaked secret: %s", got)
	}
	if !strings.Contains(got, "[redacted:api key]") {
		t.Errorf("JSON() has no redaction marker: %s", got)
	}
}

// TestStatusSnapshotCJK 确认中文与 HTML 字符按 UTF-8 原样输出，不被转义为 \uXXXX。
func TestStatusSnapshotCJK(t *testing.T) {
	snap := NewStatusSnapshot(map[string]string{"阶段": "流式输出 <>& 中文"}, nil, nil)
	got := string(snap.JSON())

	for _, want := range []string{"阶段", "流式输出 <>& 中文"} {
		if !strings.Contains(got, want) {
			t.Errorf("JSON() = %s, want to contain %q", got, want)
		}
	}
	if strings.Contains(got, `\u`) {
		t.Errorf("JSON() escaped CJK/HTML: %s", got)
	}
}

// TestStatusSnapshotByteStable 锁定重复序列化与不同插入顺序都得到相同字节。
func TestStatusSnapshotByteStable(t *testing.T) {
	snap := NewStatusSnapshot(
		map[string]string{"beta": "2", "alpha": "1", "中文": "三"},
		map[string]string{"ready": "true"},
		nil,
	)
	first := snap.JSON()
	if second := snap.JSON(); string(first) != string(second) {
		t.Errorf("JSON() not stable across calls: %s vs %s", first, second)
	}

	// 以相反插入顺序构造同样的内容；输出仍须逐字节一致。
	other := NewStatusSnapshot(
		map[string]string{"中文": "三", "alpha": "1", "beta": "2"},
		map[string]string{"ready": "true"},
		nil,
	)
	if string(other.JSON()) != string(first) {
		t.Errorf("JSON() depends on map insertion order: %s vs %s", other.JSON(), first)
	}
	if strings.HasSuffix(string(first), "\n") {
		t.Errorf("JSON() has trailing newline: %q", first)
	}
}

// TestStatusSnapshotDoesNotMutate 确认 JSON 序列化不改动快照，构造时复制入参映射。
func TestStatusSnapshotDoesNotMutate(t *testing.T) {
	turn := map[string]string{"phase": "streaming"}
	snap := NewStatusSnapshot(turn, nil, nil)

	before := string(snap.JSON())
	turn["phase"] = "mutated" // 构造后修改入参不应影响快照。
	if got := string(snap.JSON()); got != before {
		t.Errorf("JSON() changed after mutating input map: %s -> %s", before, got)
	}
	if snap.Turn == nil || snap.Turn.Values["phase"] != "streaming" {
		t.Errorf("snapshot was mutated via input map: %+v", snap.Turn)
	}
}
