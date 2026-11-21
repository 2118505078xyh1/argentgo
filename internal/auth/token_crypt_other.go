//go:build !windows

package auth

// 非 Windows 平台沿用明文存储：文件模式（0600）已提供隔离，且无 DPAPI 可用。
// 保留与 Windows 相同的函数签名，使 store.go 与 GOOS=linux 交叉编译共用一套代码。

// encryptToken 在非 Windows 平台不做加密，原样返回 secret。
func encryptToken(plain string) (string, error) {
	return plain, nil
}

// decryptToken 在非 Windows 平台原样返回存储值（含旧明文与历史数据）。
func decryptToken(stored string) (string, error) {
	return stored, nil
}
