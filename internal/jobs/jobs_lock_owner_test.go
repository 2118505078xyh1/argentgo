package jobs

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestLockOwnerInfoCompleteInfo 验证完整信息下三个字段都存在、类型正确且值不被改写。
func TestLockOwnerInfoCompleteInfo(t *testing.T) {
	info := lockOwnerInfo(4321, "v0.8.0", 1_700_000_000)

	if len(info) != 3 {
		t.Fatalf("字段数量 = %d, want 3 (%v)", len(info), info)
	}
	pid, ok := info["pid"].(int)
	if !ok || pid != 4321 {
		t.Fatalf("pid = %v (%T), want int 4321", info["pid"], info["pid"])
	}
	version, ok := info["version"].(string)
	if !ok || version != "v0.8.0" {
		t.Fatalf("version = %v (%T), want string \"v0.8.0\"", info["version"], info["version"])
	}
	startedAt, ok := info["started_at"].(int64)
	if !ok || startedAt != 1_700_000_000 {
		t.Fatalf("started_at = %v (%T), want int64 1700000000", info["started_at"], info["started_at"])
	}
}

// TestLockOwnerInfoCJKVersion 验证 CJK 版本字符串被原样保留，且经过
// JSON 序列化/反序列化后不丢失、不被转义破坏（锁文件通常以 JSON 落盘）。
func TestLockOwnerInfoCJKVersion(t *testing.T) {
	const version = "寒弦问月夜未阑 0.8.0 β版"
	info := lockOwnerInfo(123, version, 99)

	if got := info["version"]; got != version {
		t.Fatalf("version = %q, want %q", got, version)
	}

	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if got := decoded["version"]; got != version {
		t.Fatalf("序列化后 version = %v, want %q", got, version)
	}
}

// TestLockOwnerInfoFieldsStable 验证字段名固定、不产生多余键，且同一输入
// 重复调用得到相同的 map 和相同的 JSON 序列化结果。
func TestLockOwnerInfoFieldsStable(t *testing.T) {
	first := lockOwnerInfo(7, "v1", 42)
	wantKeys := map[string]bool{"pid": true, "version": true, "started_at": true}
	if len(first) != len(wantKeys) {
		t.Fatalf("字段数量 = %d, want %d (%v)", len(first), len(wantKeys), first)
	}
	for key := range first {
		if !wantKeys[key] {
			t.Fatalf("出现意外字段 %q", key)
		}
	}

	second := lockOwnerInfo(7, "v1", 42)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("同一输入结果不稳定: %v != %v", first, second)
	}

	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("json.Marshal(first): %v", err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatalf("json.Marshal(second): %v", err)
	}
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("序列化结果不稳定: %s != %s", firstJSON, secondJSON)
	}
}

// TestLockOwnerInfoZeroAndNegative 验证零值与负值被原样记录，而不是被
// 裁剪或替换；这使诊断能够区分“未知”与真实的负数值。
func TestLockOwnerInfoZeroAndNegative(t *testing.T) {
	tests := []struct {
		name      string
		pid       int
		version   string
		startedAt int64
	}{
		{name: "全零值", pid: 0, version: "", startedAt: 0},
		{name: "负 pid", pid: -1, version: "v0", startedAt: 100},
		{name: "负启动时间", pid: 9, version: "v0", startedAt: -5},
		{name: "全部负值", pid: -404, version: "未知版本", startedAt: -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := lockOwnerInfo(tt.pid, tt.version, tt.startedAt)
			if got := info["pid"]; got != tt.pid {
				t.Fatalf("pid = %v, want %d", got, tt.pid)
			}
			if got := info["version"]; got != tt.version {
				t.Fatalf("version = %v, want %q", got, tt.version)
			}
			if got := info["started_at"]; got != tt.startedAt {
				t.Fatalf("started_at = %v, want %d", got, tt.startedAt)
			}
		})
	}
}
