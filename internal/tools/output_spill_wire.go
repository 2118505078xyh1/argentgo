package tools

import (
	"context"
	"os"
	"strconv"
	"strings"
)

// 本文件把 SpillLargeOutput 接入工具输出流水线，而不
// 改变任何现有输出格式。溢出严格是 opt-in：除非调用方启用，否则
// 普通的 clip()/clipTo() 路径不受影响，因此默认
// 行为（以及所有现有测试）逐字节一致。

const (
	// DefaultOutputSpillThreshold 是调用方启用溢出时使用的保守的
	// "超大"上限。它远高于 maxToolOutput（40 KB），因此
	// 普通的大型输出仍保持既有的截断格式。
	DefaultOutputSpillThreshold = 200 << 10 // 200 KiB

	// spillDirEnv 和 spillThresholdEnv 允许无需任何代码
	// 改动即可启用溢出。两者都必须设置，env 开关才会生效；两者都
	// 未设置时，溢出被禁用。
	spillDirEnv       = "ARGENT_OUTPUT_SPILL_DIR"
	spillThresholdEnv = "ARGENT_OUTPUT_SPILL_THRESHOLD"
)

// spillSettings 是随 context 携带的 opt-in 配置。
type spillSettings struct {
	threshold int
	dir       string
}

// enabled 报告是否应执行溢出。非正的阈值或
// 空目录会让功能保持关闭，因此半配置的开关绝不会
// 把产出写到意料之外的位置。
func (s spillSettings) enabled() bool {
	return s.threshold > 0 && strings.TrimSpace(s.dir) != ""
}

type spillConfigKey struct{}

// WithOutputSpill 返回一个派生 context，让工具输出选择
// 产出溢出。大于 threshold 的输出会被完整写入
// dir 下，并替换为带有恢复链接的有界头+尾视图。
// 显式传入非正阈值或空目录会为该 context 明确禁用
// 溢出，覆盖 env 开关。
func WithOutputSpill(ctx context.Context, threshold int, dir string) context.Context {
	return context.WithValue(ctx, spillConfigKey{}, spillSettings{threshold: threshold, dir: dir})
}

// spillConfig 解析 opt-in 配置。显式的 context 值优先；
// 否则检查两个环境变量。默认：禁用。
func spillConfig(ctx context.Context) spillSettings {
	if s, ok := ctx.Value(spillConfigKey{}).(spillSettings); ok {
		return s
	}
	threshold, err := strconv.Atoi(strings.TrimSpace(os.Getenv(spillThresholdEnv)))
	if err != nil {
		threshold = 0
	}
	return spillSettings{threshold: threshold, dir: strings.TrimSpace(os.Getenv(spillDirEnv))}
}

// spillToolOutput 是中央的规范化钩子。它报告自己是否
// 处理了 text；当它返回 handled == false 时，调用方必须回退到
// 普通的 clipTo 路径。处理涵盖三种情况：
//   - 溢出关闭 -> 不处理（行为不变）；
//   - 文本带有指向我们某个产出的完整溢出标记 ->
//     被原样返回，因此溢出结果绝不会被二次溢出或二次截断；
//     不可信输出中形似标记的子串不受信任；
//   - 文本超过阈值 -> 溢出并替换为有界视图。
//
// 小于或等于阈值的文本有意不在此处理：普通的
// clipTo 上限仍然适用，因此提高溢出阈值绝不会悄悄
// 让中等大小的输出未经截断进入模型上下文。
func spillToolOutput(ctx context.Context, text string) (out string, handled bool) {
	cfg := spillConfig(ctx)
	if !cfg.enabled() {
		return "", false
	}
	if isOwnSpillResult(text, cfg.dir) {
		return text, true
	}
	if len(text) <= cfg.threshold {
		return "", false
	}
	result, _, _ := SpillLargeOutput(text, cfg.threshold, cfg.dir)
	return result, true
}
