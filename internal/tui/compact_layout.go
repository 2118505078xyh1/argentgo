package tui

// 紧凑布局的尺寸阈值。审计发现小终端应隐藏侧栏以避免挤压正文，
// 这里把“小终端”的判定收敛为两个常量，便于接线点与测试共享同一口径。
const (
	compactLayoutMinWidth  = 120
	compactLayoutMinHeight = 30
)

// CompactLayout 是紧凑布局的 opt-in 开关，默认关闭，使现有固定布局
// 与字节级渲染测试保持逐字一致。打开后，接线点（render.go 的 layoutLocked）
// 在小终端上隐藏侧栏。保留为普通包变量，方便以后接到 CLI 标志或配置
// 而无需改动渲染器。
var CompactLayout = false

// shouldUseCompactLayout 判断给定终端尺寸是否应进入紧凑模式。
// 宽小于 compactLayoutMinWidth 或高小于 compactLayoutMinHeight 即返回 true。
// 该函数是纯函数：零值与负值会自然落入“小终端”一侧（例如 0 < 120），
// 因此无效或极小尺寸被视为紧凑，调用方无需额外归一化。
func shouldUseCompactLayout(width, height int) bool {
	return width < compactLayoutMinWidth || height < compactLayoutMinHeight
}

// panelsHiddenInCompactLayout 判断在当前 opt-in 开关与尺寸下侧栏是否应被隐藏。
// 它供 render.go 的 layoutLocked 接线使用，把开关与尺寸判定集中在一处：
//
//	if m.panelsVisible() && !panelsHiddenInCompactLayout(width, height) {
//
// 开关默认关闭时本函数恒返回 false，因此接线不改变任何现有行为。
func panelsHiddenInCompactLayout(width, height int) bool {
	return CompactLayout && shouldUseCompactLayout(width, height)
}
