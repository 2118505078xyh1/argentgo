// Package computer 提供桌面级 computer-use 能力：屏幕截图与鼠标键盘输入。
//
// 设计约束（与 browser 包一致）：
//   - 只用 Go 标准库，syscall 动态调用 Win32 DLL，无 cgo、无第三方依赖。
//   - 所有代码必须能用 GOOS=linux go build ./... 交叉编译，因此 Windows
//     实现放在 //go:build windows 文件中，其余平台由 other.go 提供 stub。
//   - 不做 AX/UIAutomation 可访问性树：那是 COM，纯 syscall 手写 vtable
//     成本极高且易崩。采用「截图 + 坐标点击」路线，与 browser 的 @N refs
//     互补：Web 用 browser，原生桌面应用用 computer。
package computer
