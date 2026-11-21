//go:build windows

package auth

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"syscall"
	"unsafe"
)

// Windows 上用 DPAPI（CryptProtectData/CryptUnprotectData）按当前用户身份封装令牌。
// DPAPI 由操作系统提供，经 syscall.NewLazyDLL 动态调用 crypt32.dll（这两个函数实际由
// crypt32.dll 导出，而非 advapi32.dll），零外部依赖、无 cgo。
// 密文受当前用户凭据保护，绕过了 os.Chmod(0600) 在 Windows 上不设置 DACL 导致的文件权限失效问题。

// CRYPTPROTECT_UI_FORBIDDEN：禁止弹出任何 UI，非交互会话（如服务/CI）不会卡住。
const cryptProtectUIForbidden = 0x00000001

var (
	crypt32                = syscall.NewLazyDLL("crypt32.dll")
	procCryptProtectData   = crypt32.NewProc("CryptProtectData")
	procCryptUnprotectData = crypt32.NewProc("CryptUnprotectData")
)

// dataBlob 对应 Win32 DATA_BLOB { DWORD cbData; BYTE* pbData }。
type dataBlob struct {
	cbData uint32
	pbData *byte
}

// encryptToken 用 DPAPI 封装 secret，返回带版本前缀的 base64 密文。
func encryptToken(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	protected, err := protectData([]byte(plain))
	if err != nil {
		return "", err
	}
	return tokenSealPrefix + base64.StdEncoding.EncodeToString(protected), nil
}

// decryptToken 解封 dpapi:v1: 前缀的密文；无前缀的旧明文原样返回（迁移期兼容）。
func decryptToken(stored string) (string, error) {
	if !strings.HasPrefix(stored, tokenSealPrefix) {
		return stored, nil
	}
	encoded := strings.TrimPrefix(stored, tokenSealPrefix)
	blob, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("auth: 解码受保护令牌失败: %w", err)
	}
	plain, err := unprotectData(blob)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// protectData 调用 CryptProtectData，复制出结果后释放 DPAPI 分配的内存。
func protectData(plain []byte) ([]byte, error) {
	in := dataBlob{cbData: uint32(len(plain))}
	if len(plain) > 0 {
		in.pbData = &plain[0]
	}
	var out dataBlob
	// BOOL CryptProtectData(DATA_BLOB*, LPCWSTR, DATA_BLOB*, PVOID,
	//                       CRYPT_PROMPTSTRUCT*, DWORD, DATA_BLOB*)
	r1, _, e1 := procCryptProtectData.Call(
		uintptr(unsafe.Pointer(&in)),
		0, // pszDataDescr
		0, // pOptionalEntropy
		0, // pvReserved
		0, // pPromptStruct
		uintptr(cryptProtectUIForbidden),
		uintptr(unsafe.Pointer(&out)),
	)
	if r1 == 0 {
		return nil, dpapiError("CryptProtectData", e1)
	}
	if out.pbData == nil || out.cbData == 0 {
		return nil, errors.New("auth: CryptProtectData 返回空缓冲区")
	}
	// 复制后必须 LocalFree，否则泄漏 DPAPI 用 LocalAlloc 分配的内存。
	defer syscall.LocalFree(syscall.Handle(uintptr(unsafe.Pointer(out.pbData))))
	return append([]byte(nil), unsafe.Slice(out.pbData, int(out.cbData))...), nil
}

// unprotectData 调用 CryptUnprotectData 还原明文，同样负责释放输出缓冲区。
func unprotectData(blob []byte) ([]byte, error) {
	in := dataBlob{cbData: uint32(len(blob))}
	if len(blob) > 0 {
		in.pbData = &blob[0]
	}
	var out dataBlob
	// BOOL CryptUnprotectData(DATA_BLOB*, LPWSTR*, DATA_BLOB*, PVOID,
	//                         CRYPT_PROMPTSTRUCT*, DWORD, DATA_BLOB*)
	r1, _, e1 := procCryptUnprotectData.Call(
		uintptr(unsafe.Pointer(&in)),
		0, // ppszDataDescr
		0, // pOptionalEntropy
		0, // pvReserved
		0, // pPromptStruct
		uintptr(cryptProtectUIForbidden),
		uintptr(unsafe.Pointer(&out)),
	)
	if r1 == 0 {
		return nil, dpapiError("CryptUnprotectData", e1)
	}
	if out.pbData == nil {
		return nil, errors.New("auth: CryptUnprotectData 返回空缓冲区")
	}
	defer syscall.LocalFree(syscall.Handle(uintptr(unsafe.Pointer(out.pbData))))
	return append([]byte(nil), unsafe.Slice(out.pbData, int(out.cbData))...), nil
}

// dpapiError 把 Call 返回的错误包装成可读信息；Errno 为零的哨兵值不算真实错误。
func dpapiError(api string, lastErr error) error {
	if lastErr != nil && lastErr != syscall.Errno(0) {
		return fmt.Errorf("auth: %s: %w", api, lastErr)
	}
	return fmt.Errorf("auth: %s 调用失败", api)
}
