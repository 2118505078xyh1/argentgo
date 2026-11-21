package jobs

import (
	"reflect"
	"sort"
	"testing"
)

// subtreeJob 只设置 ID 构造最小作业，让子树测试聚焦于 ID 层级本身。
func subtreeJob(id string) JobInfo {
	return JobInfo{ID: id}
}

// TestTasksInSubtreeEmptyAndBoundary 覆盖空输入与常见边界：
// nil、空切片、找不到父作业、以及父作业自身不能算作自己的后代。
func TestTasksInSubtreeEmptyAndBoundary(t *testing.T) {
	tests := []struct {
		name     string
		jobs     []JobInfo
		parentID string
		want     []string
	}{
		{"nil 作业切片", nil, "parent", []string{}},
		{"空作业切片", []JobInfo{}, "parent", []string{}},
		{"父作业不存在", []JobInfo{subtreeJob("other/child")}, "parent", []string{}},
		{"只有父作业自身", []JobInfo{subtreeJob("parent")}, "parent", []string{}},
		{"空父 ID 不匹配普通层级 ID", []JobInfo{subtreeJob("a/b"), subtreeJob("c/d")}, "", []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tasksInSubtree(tt.jobs, tt.parentID)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("tasksInSubtree() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestTasksInSubtreeDirectChildren 验证只收集带分隔符前缀的直接/旁支后代，
// 排除同前缀但不构成父子关系的 ID，以及父作业本身。
func TestTasksInSubtreeDirectChildren(t *testing.T) {
	jobs := []JobInfo{
		subtreeJob("job"),          // 父作业自身，排除
		subtreeJob("job/alpha"),    // 直接子作业
		subtreeJob("job/beta"),     // 直接子作业
		subtreeJob("jobber/child"), // 仅字符串前缀相同，非后代
		subtreeJob("other/child"),  // 无关子树
	}
	want := []string{"job/alpha", "job/beta"}
	if got := tasksInSubtree(jobs, "job"); !reflect.DeepEqual(got, want) {
		t.Fatalf("tasksInSubtree() = %v, want %v", got, want)
	}
}

// TestTasksInSubtreeMultiLevel 验证多层后代都会被收集，即使中间层作业
// 并未出现在入参中（例如只有 "root/a/b" 而没有 "root/a"）。
func TestTasksInSubtreeMultiLevel(t *testing.T) {
	jobs := []JobInfo{
		subtreeJob("root"),
		subtreeJob("root/a"),
		subtreeJob("root/a/b"),
		subtreeJob("root/a/b/c"), // 中间层缺失也应算作 root 的后代
		subtreeJob("root/z"),
		subtreeJob("root2/child"), // 同前缀但不同子树
	}
	want := []string{"root/a", "root/a/b", "root/a/b/c", "root/z"}
	if got := tasksInSubtree(jobs, "root"); !reflect.DeepEqual(got, want) {
		t.Fatalf("tasksInSubtree() = %v, want %v", got, want)
	}
}

// TestTasksInSubtreeStableCJKOrder 验证含 CJK 字符的 ID 按字节序稳定排序，
// 且结果与入参顺序无关。
func TestTasksInSubtreeStableCJKOrder(t *testing.T) {
	want := []string{"任务/上海", "任务/北京", "任务/广州", "任务/成都"}
	// want 自身按字节序排列，作为排序基准的断言。
	if sorted := append([]string(nil), want...); !sort.StringsAreSorted(sorted) {
		t.Fatalf("测试基准未按字节序排列: %v", sorted)
	}

	jobs := []JobInfo{
		subtreeJob("任务/成都"),
		subtreeJob("任务/北京"),
		subtreeJob("任务/广州"),
		subtreeJob("任务/上海"),
	}
	if got := tasksInSubtree(jobs, "任务"); !reflect.DeepEqual(got, want) {
		t.Fatalf("乱序入参 tasksInSubtree() = %v, want %v", got, want)
	}

	// 反转入参后结果必须保持一致（稳定性 / 与输入顺序无关）。
	reversed := make([]JobInfo, len(jobs))
	for i, job := range jobs {
		reversed[len(jobs)-1-i] = job
	}
	if got := tasksInSubtree(reversed, "任务"); !reflect.DeepEqual(got, want) {
		t.Fatalf("反转入参 tasksInSubtree() = %v, want %v", got, want)
	}
}

// TestTasksInSubtreeDoesNotMutateInput 验证纯函数语义：调用前后入参切片
// 的内容与顺序均保持不变。
func TestTasksInSubtreeDoesNotMutateInput(t *testing.T) {
	jobs := []JobInfo{
		subtreeJob("p/z"),
		subtreeJob("q/other"),
		subtreeJob("p/a"),
	}
	original := append([]JobInfo(nil), jobs...)

	_ = tasksInSubtree(jobs, "p")

	if !reflect.DeepEqual(jobs, original) {
		t.Fatalf("入参被修改: got %v, want %v", jobs, original)
	}
}
