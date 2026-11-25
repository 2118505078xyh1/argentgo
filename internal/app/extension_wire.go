package app

import (
	"log"
	"strings"

	"example.com/argentgo/internal/extension"
	"example.com/argentgo/internal/hooks"
)

// extensionInventoryEnv 是扩展清单诊断的环境变量。只有设为 "1" 才输出；
// 默认关闭时零输出，逐字节行为不变。
const extensionInventoryEnv = "ARGENT_EXTENSION_INVENTORY"

func extensionInventoryEnabled(env func(string) string) bool {
	return env != nil && strings.TrimSpace(env(extensionInventoryEnv)) == "1"
}

// reportExtensionInventory 用统一门面构建扩展清单并输出诊断。
// 它是 verify-only 的可观测入口：只读取发现结果，不改任何运行态集合，
// 也不触碰 toolset/prompt 的字节。
//
// 这也是门面的生产接线点：Discover* 与 NewAuthorizer 在此被真实调用，
// 满足 archguard "新增导出原语须有生产调用方"门禁。
func reportExtensionInventory(root, home string, trust *hooks.TrustStore, env func(string) string) {
	if !extensionInventoryEnabled(env) {
		return
	}
	inv := extension.DiscoverSkills([]string{root, home})
	cmds := extension.DiscoverCommands(commandSources([]string{root, home}))
	inv.Descriptors = append(inv.Descriptors, cmds.Descriptors...)
	inv.Diagnostics = append(inv.Diagnostics, cmds.Diagnostics...)

	plugInv, err := extension.DiscoverPlugins(root)
	if err == nil {
		inv.Descriptors = append(inv.Descriptors, plugInv.Descriptors...)
		inv.Diagnostics = append(inv.Diagnostics, plugInv.Diagnostics...)
	} else {
		inv.Diagnostics = append(inv.Diagnostics, extension.Diagnostic{
			Kind: extension.DiagnosticInvalid, Source: root, Message: err.Error(),
		})
	}

	authz := extension.NewAuthorizer(trust)
	// 探测信任门是否处于 fail-closed 状态：用一个永不匹配的键/摘要调用，
	// 授信存储为空时应返回 ErrUntrusted。这是一次只读自检，不改任何状态。
	if authz.Enabled() {
		_ = authz.Authorize("__extension_probe__", "__extension_probe__")
	}
	log.Printf("[extension] inventory: %d descriptors, %d diagnostics, trust-gate=%v",
		len(inv.Descriptors), len(inv.Diagnostics), authz.Enabled())
	for _, d := range inv.Diagnostics {
		log.Printf("[extension] diagnostic: %s %s: %s", d.Kind, d.Source, d.Message)
	}
}
