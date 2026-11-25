package app

import (
	"example.com/argentgo/internal/agent"
	"example.com/argentgo/internal/session"
)

// 本文件承载 TODO #28 的 app 侧接线：Execute 入口写 working，按结果写 done/error，
// 会话退出时 Delete。注册表与 Sessions 返回的存储适配器共享同一实例，因此
// session.list 能立即观察到状态变化。会话 id 为空（尚未创建的伪会话）时全为空操作。

// beginSessionStatus 在 Execute 入口把会话标记为 working，并返回按最终错误收尾的
// 函数。Execute 需要使用命名返回值 err 接线：
//
//	finish := beginSessionStatus(sess)
//	defer func() { finish(err) }()
func beginSessionStatus(sess *session.Session) func(err error) {
	if sess == nil || sess.ID() == "" {
		return func(error) {}
	}
	id := sess.ID()
	sessionStatusRegistry.Set(id, agent.StatusWorking)
	return func(err error) {
		status := agent.StatusDone
		if err != nil {
			status = agent.StatusError
		}
		sessionStatusRegistry.Set(id, status)
	}
}

// clearSessionStatus 在会话退出或被放弃时移除记录；之后查询返回 idle。
func clearSessionStatus(sess *session.Session) {
	if sess == nil || sess.ID() == "" {
		return
	}
	sessionStatusRegistry.Delete(sess.ID())
}
