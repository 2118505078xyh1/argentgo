package tools

import (
	"context"
	"testing"

	"example.com/argentgo/internal/jobs"
)

// TestSetJobsInstallsRegistry 锁定 app 接线所依赖的接缝：默认无作业注册表，
// SetJobs 安装后工具可通过 ctx 取到同一实例，background_after_ms 才能生效。
func TestSetJobsInstallsRegistry(t *testing.T) {
	r := New(t.TempDir(), true)
	if r.jobs != nil {
		t.Fatal("新注册表默认不应带作业注册表")
	}
	if got := jobRegistry(context.Background()); got != nil {
		t.Fatalf("未注入时 jobRegistry = %v, want nil", got)
	}

	reg, err := jobs.New(t.TempDir())
	if err != nil {
		t.Fatalf("new jobs registry: %v", err)
	}
	r.SetJobs(reg)
	if r.jobs != reg {
		t.Fatal("SetJobs 未安装传入的注册表")
	}

	// 复现 Registry.Run 的注入路径，确认接线后工具能取到同一个注册表。
	wired := context.WithValue(context.Background(), jobsKey{}, r.jobs)
	if got := jobRegistry(wired); got != reg {
		t.Fatalf("接线后 jobRegistry = %v, want %v", got, reg)
	}
}
