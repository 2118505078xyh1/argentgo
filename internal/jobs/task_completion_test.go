package jobs

import (
	"reflect"
	"testing"
)

// statJob 用最小字段构造一个作业，便于统计测试聚焦状态与退出码。
func statJob(id string, status Status, exitCode int) JobInfo {
	return JobInfo{ID: id, Status: status, ExitCode: exitCode}
}

// statNamedJob 在 statJob 的基础上设置命令名，用于失败清单与 CJK 测试。
func statNamedJob(id, command string, status Status, exitCode int) JobInfo {
	return JobInfo{ID: id, Command: command, Status: status, ExitCode: exitCode}
}

// statCount 读取 map 中的整型统计值，类型不符直接让测试失败。
func statCount(t *testing.T, stats map[string]any, key string) int {
	t.Helper()
	value, ok := stats[key].(int)
	if !ok {
		t.Fatalf("键 %q 的类型为 %T，期望 int", key, stats[key])
	}
	return value
}

// statRate 读取 map 中的浮点统计值。
func statRate(t *testing.T, stats map[string]any, key string) float64 {
	t.Helper()
	value, ok := stats[key].(float64)
	if !ok {
		t.Fatalf("键 %q 的类型为 %T，期望 float64", key, stats[key])
	}
	return value
}

// statNames 读取 map 中的失败名清单。
func statNames(t *testing.T, stats map[string]any, key string) []string {
	t.Helper()
	value, ok := stats[key].([]string)
	if !ok {
		t.Fatalf("键 %q 的类型为 %T，期望 []string", key, stats[key])
	}
	return value
}

// assertCounts 一次性校验所有计数类键与成功率。
func assertCounts(t *testing.T, stats map[string]any, total, running, exited, killed, succeeded, failed, completed int, rate float64) {
	t.Helper()
	checks := []struct {
		key  string
		want int
	}{
		{"total", total},
		{"running", running},
		{"exited", exited},
		{"killed", killed},
		{"succeeded", succeeded},
		{"failed", failed},
		{"completed", completed},
	}
	for _, check := range checks {
		if got := statCount(t, stats, check.key); got != check.want {
			t.Errorf("%s = %d，期望 %d", check.key, got, check.want)
		}
	}
	if got := statRate(t, stats, "successRate"); got != rate {
		t.Errorf("successRate = %v，期望 %v", got, rate)
	}
}

func TestTaskCompletionStatsMixedStatuses(t *testing.T) {
	jobs := []JobInfo{
		statNamedJob("job1", "build", StatusRunning, -1),
		statNamedJob("job2", "test", StatusExited, 0),
		statNamedJob("job3", "deploy", StatusExited, 2),
		statNamedJob("job4", "cleanup", StatusKilled, -1),
		statNamedJob("job5", "fetch", StatusExited, -1),
	}
	stats := taskCompletionStats(jobs)

	// running 1，exited 3，killed 1；成功 1，失败 3，完成 4，成功率 1/4。
	assertCounts(t, stats, 5, 1, 3, 1, 1, 3, 4, 0.25)
	// 失败清单按字节序排序：cleanup < deploy < fetch。
	want := []string{"cleanup", "deploy", "fetch"}
	if got := statNames(t, stats, "failedNames"); !reflect.DeepEqual(got, want) {
		t.Errorf("failedNames = %v，期望 %v", got, want)
	}
}

func TestTaskCompletionStatsAllSucceeded(t *testing.T) {
	jobs := []JobInfo{
		statJob("job1", StatusExited, 0),
		statJob("job2", StatusExited, 0),
		statJob("job3", StatusExited, 0),
	}
	stats := taskCompletionStats(jobs)

	assertCounts(t, stats, 3, 0, 3, 0, 3, 0, 3, 1)
	if got := statNames(t, stats, "failedNames"); len(got) != 0 {
		t.Errorf("failedNames = %v，期望空清单", got)
	}
}

func TestTaskCompletionStatsAllFailed(t *testing.T) {
	jobs := []JobInfo{
		statNamedJob("job1", "编译", StatusExited, 1),
		statNamedJob("job2", "部署", StatusKilled, -1),
		statNamedJob("job3", "抓取", StatusExited, -1),
	}
	stats := taskCompletionStats(jobs)

	assertCounts(t, stats, 3, 0, 2, 1, 0, 3, 3, 0)
	want := []string{"抓取", "编译", "部署"} // 按 UTF-8 字节序排序。
	if got := statNames(t, stats, "failedNames"); !reflect.DeepEqual(got, want) {
		t.Errorf("failedNames = %v，期望 %v", got, want)
	}
}

func TestTaskCompletionStatsEmpty(t *testing.T) {
	for _, tc := range []struct {
		name string
		jobs []JobInfo
	}{
		{"nil", nil},
		{"空切片", []JobInfo{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stats := taskCompletionStats(tc.jobs)
			assertCounts(t, stats, 0, 0, 0, 0, 0, 0, 0, 0)
			names := statNames(t, stats, "failedNames")
			if names == nil {
				t.Error("failedNames 不应为 nil，便于编码为 JSON 的 []")
			}
			if len(names) != 0 {
				t.Errorf("failedNames = %v，期望空清单", names)
			}
		})
	}
}

func TestTaskCompletionStatsCJKNamesStable(t *testing.T) {
	// 同一组作业以两种顺序传入，失败清单必须完全一致。
	first := []JobInfo{
		statNamedJob("a", "中文任务", StatusExited, 1),
		statNamedJob("b", "日本語タスク", StatusKilled, -1),
		statNamedJob("c", "это задача", StatusExited, 3),
		statNamedJob("d", "ascii-task", StatusExited, 4),
	}
	second := []JobInfo{first[3], first[2], first[1], first[0]}

	gotFirst := statNames(t, taskCompletionStats(first), "failedNames")
	gotSecond := statNames(t, taskCompletionStats(second), "failedNames")
	if !reflect.DeepEqual(gotFirst, gotSecond) {
		t.Fatalf("失败清单随输入顺序变化：%v vs %v", gotFirst, gotSecond)
	}
	// 字节序：ascii(0x61) < это(0xD1) < 中文(0xE4) < 日本語(0xE6)。
	want := []string{"ascii-task", "это задача", "中文任务", "日本語タスク"}
	if !reflect.DeepEqual(gotFirst, want) {
		t.Errorf("failedNames = %v，期望 %v", gotFirst, want)
	}
}

func TestTaskCompletionStatsSuccessRateExcludesRunning(t *testing.T) {
	// 成功率只按已完成作业计算：3 成功 / (3 成功 + 1 失败) = 0.75，
	// 两个 running 作业既不成功也不失败，不进入分母。
	jobs := []JobInfo{
		statJob("r1", StatusRunning, -1),
		statJob("r2", StatusRunning, 0),
		statJob("s1", StatusExited, 0),
		statJob("s2", StatusExited, 0),
		statJob("s3", StatusExited, 0),
		statJob("f1", StatusExited, 7),
	}
	stats := taskCompletionStats(jobs)

	assertCounts(t, stats, 6, 2, 4, 0, 3, 1, 4, 0.75)
}

func TestTaskCompletionStatsDoesNotMutateInput(t *testing.T) {
	jobs := []JobInfo{
		statNamedJob("z", "zeta", StatusExited, 1),
		statNamedJob("a", "alpha", StatusKilled, -1),
	}
	original := append([]JobInfo(nil), jobs...)

	taskCompletionStats(jobs)

	if !reflect.DeepEqual(jobs, original) {
		t.Errorf("入参被修改：%v，原始 %v", jobs, original)
	}
}

func TestTaskCompletionStatsJobNameFallback(t *testing.T) {
	// 命令为纯空白时回退到 ID，有内容的命令保持原值。
	jobs := []JobInfo{
		statNamedJob("job-empty", "   ", StatusExited, 1),
		statJob("job-nocmd", StatusKilled, -1),
	}
	stats := taskCompletionStats(jobs)

	want := []string{"job-empty", "job-nocmd"}
	if got := statNames(t, stats, "failedNames"); !reflect.DeepEqual(got, want) {
		t.Errorf("failedNames = %v，期望 %v", got, want)
	}
}
