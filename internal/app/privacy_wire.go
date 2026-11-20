package app

import (
	"fmt"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// ApplyPrivacyPosture 对一次运行强制执行已解析的隐私姿态。它是 app 层的桥梁，
// 使 security.PrivacyMode 的辅助函数能被生产代码调用：config.Config.PrivacyMode
// 从 privacyMode / privacy_mode 键、ARGENT_PRIVACY_MODE 和 --privacy-mode 解码而来，
// 本函数把该姿态转化为具体的决定。
//
// 当模式为 local-only 时，它返回必须从模型中保留的 webToolNames 子集。
// 空模式按默认的 "any" 姿态处理（空操作）。local-only 模式下使用远程 baseURL，
// 或未知模式，都会报错，使错误不会静默地放宽姿态。
//
// 接线说明（给 app_execute.go 的负责人）：在过滤工具的地方调用它一次，
// 即 Execute 中 FilterSpecs 闭包的位置，使用已解析好的值：
//
//	blocked, err := ApplyPrivacyPosture(res.Cfg.PrivacyMode, res.BaseURL, webToolNames)
//	if err != nil {
//	    return err
//	}
//	// 然后在返回之前，从公布的工具规格中去掉 `blocked`。
//
// 仅检查 base URL 还不够：web_search / web_fetch 可以独立于聊天端点访问网络，
// 因此这两项决定都属于这里。
func ApplyPrivacyPosture(mode string, baseURL string, webToolNames []string) (blockedWebTools []string, err error) {
	privacyMode := security.PrivacyMode(strings.ToLower(strings.TrimSpace(mode)))
	if privacyMode == "" {
		privacyMode = security.PrivacyAny
	}

	allowed, reason := security.AllowRemote(privacyMode, baseURL, false)
	if !allowed {
		return nil, fmt.Errorf("app: privacy: base URL denied: %s", reason)
	}

	if security.ShouldBlockWebTool(privacyMode) {
		blockedWebTools = append([]string(nil), webToolNames...)
	}
	return blockedWebTools, nil
}
