package tools_test

import (
	"context"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/tools"
)

// foreignPlanUnlockKey 模拟"另一个包里同名同结构的 context key"。
// Go 的 context key 靠类型身份匹配：这个外部类型与 tools 包私有的
// planUnlockKey 不是同一个类型，因此互相读不到对方写入的值。
type foreignPlanUnlockKey struct{}

func TestExitPlanContextKeyCrossPackageIdentity(t *testing.T) {
	// 外部包用自己的 key 写入回调，tools.PlanUnlockFrom 不应读到。
	foreignCalled := false
	foreignCtx := context.WithValue(context.Background(), foreignPlanUnlockKey{}, func() {
		foreignCalled = true
	})
	if fn := tools.PlanUnlockFrom(foreignCtx); fn != nil {
		t.Fatal("PlanUnlockFrom must not read a foreign package's key")
	}

	// 把 foreignCtx 传给真正的 exit_plan：没有 SetPlanUnlock 时仍必须报错，
	// 证明外部 key 无法完成解锁。
	r := tools.New(t.TempDir(), true)
	tools.RegisterExitPlan(r)
	_, err := r.Run(foreignCtx, "exit_plan", `{"plan":"审阅并批准"}`)
	if err == nil || !strings.Contains(err.Error(), "no plan unlock callback") {
		t.Fatalf("err = %v, want missing-callback diagnostic (foreign key must not unlock)", err)
	}
	if foreignCalled {
		t.Fatal("foreign callback must not be invoked")
	}

	// 正常接线路径：SetPlanUnlock 注入的值能被工具读到并调用。
	realCalled := false
	r.SetPlanUnlock(func() { realCalled = true })
	if _, err := r.Run(context.Background(), "exit_plan", `{"plan":"审阅并批准"}`); err != nil {
		t.Fatalf("wired run: %v", err)
	}
	if !realCalled {
		t.Fatal("wired callback was not invoked")
	}
}
