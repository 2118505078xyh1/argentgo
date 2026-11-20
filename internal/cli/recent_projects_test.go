package cli

import (
	"reflect"
	"testing"
	"time"
)

// recentEntry 用整秒偏移构造一条 ProjectEntry，便于在测试中比较访问先后。
func recentEntry(path string, sec int64) ProjectEntry {
	return ProjectEntry{Path: path, LastAccessed: time.Unix(sec, 0)}
}

func TestRecentProjectsMultipleProjects(t *testing.T) {
	entries := []ProjectEntry{
		recentEntry("/work/alpha", 1),
		recentEntry("/work/beta", 3),
		recentEntry("/work/gamma", 2),
	}
	got := recentProjects(entries, 3)
	want := []string{"/work/beta", "/work/gamma", "/work/alpha"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("recentProjects() = %v, want %v", got, want)
	}
}

func TestRecentProjectsDedupe(t *testing.T) {
	t.Run("保留每个路径最新的访问时间", func(t *testing.T) {
		// /alpha 最新访问是 5，/beta 是 3；若错误地按首次出现去重会得到 /beta 在前。
		entries := []ProjectEntry{
			recentEntry("/alpha", 1),
			recentEntry("/beta", 3),
			recentEntry("/alpha", 5),
		}
		got := recentProjects(entries, 10)
		want := []string{"/alpha", "/beta"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("recentProjects() = %v, want %v", got, want)
		}
	})

	t.Run("重复路径只出现一次", func(t *testing.T) {
		entries := []ProjectEntry{
			recentEntry("/dup", 1),
			recentEntry("/other", 4),
			recentEntry("/dup", 2),
			recentEntry("/dup", 3),
		}
		got := recentProjects(entries, 10)
		want := []string{"/other", "/dup"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("recentProjects() = %v, want %v", got, want)
		}
		seen := map[string]bool{}
		for _, p := range got {
			if seen[p] {
				t.Fatalf("重复路径 %q 出现在结果中: %v", p, got)
			}
			seen[p] = true
		}
	})
}

func TestRecentProjectsLRUOrder(t *testing.T) {
	// 访问时间为 1/2/3，max=2 应淘汰最久未使用的 old。
	entries := []ProjectEntry{
		recentEntry("old", 1),
		recentEntry("mid", 2),
		recentEntry("new", 3),
	}
	got := recentProjects(entries, 2)
	want := []string{"new", "mid"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("recentProjects() = %v, want %v (LRU 顺序)", got, want)
	}
}

func TestRecentProjectsCJKPaths(t *testing.T) {
	entries := []ProjectEntry{
		recentEntry(`D:\项目\甲`, 1),
		recentEntry(`D:\项目\乙`, 3),
		// 去重对 CJK 路径同样生效；最新时间 2 仍低于乙。
		recentEntry(`D:\项目\甲`, 2),
		recentEntry(`D:\项目\丙😀`, 4),
	}
	got := recentProjects(entries, 10)
	want := []string{`D:\项目\丙😀`, `D:\项目\乙`, `D:\项目\甲`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("recentProjects() = %v, want %v", got, want)
	}
}

func TestRecentProjectsMaxBoundaries(t *testing.T) {
	entries := []ProjectEntry{
		recentEntry("a", 1),
		recentEntry("b", 2),
		recentEntry("c", 3),
	}
	tests := []struct {
		name string
		max  int
		want []string
	}{
		{name: "max 为 1 只返回最近一个", max: 1, want: []string{"c"}},
		{name: "max 等于数量返回全部", max: 3, want: []string{"c", "b", "a"}},
		{name: "max 大于数量返回全部", max: 99, want: []string{"c", "b", "a"}},
		{name: "max 为 0 返回空", max: 0, want: nil},
		{name: "max 为负数返回空", max: -5, want: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := recentProjects(entries, tc.max)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("recentProjects(max=%d) = %v, want %v", tc.max, got, tc.want)
			}
		})
	}
}

func TestRecentProjectsEmpty(t *testing.T) {
	tests := []struct {
		name    string
		entries []ProjectEntry
	}{
		{name: "nil 切片", entries: nil},
		{name: "空切片", entries: []ProjectEntry{}},
		{name: "全部为空路径", entries: []ProjectEntry{{LastAccessed: time.Unix(1, 0)}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := recentProjects(tc.entries, 10); len(got) != 0 {
				t.Fatalf("recentProjects() = %v, want empty", got)
			}
		})
	}
}

func TestRecentProjectsStable(t *testing.T) {
	// 访问时间完全相同，结果应保持输入中的先后次序。
	entries := []ProjectEntry{
		recentEntry("/first", 7),
		recentEntry("/second", 7),
		recentEntry("/third", 7),
	}
	got := recentProjects(entries, 10)
	want := []string{"/first", "/second", "/third"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("recentProjects() = %v, want %v (稳定次序)", got, want)
	}
}
