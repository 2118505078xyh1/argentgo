package tui

import "testing"

func TestMouseIsPrimary(t *testing.T) {
	cases := []struct {
		button int
		want   bool
	}{
		{0, true},   // 左键按下
		{2, false},  // 右键按下
		{1, false},  // 中键
		{32, true},  // 左键拖动（0x20 标志位不影响低两位）
		{34, false}, // 右键拖动
		{64, false}, // 滚轮（不应走选择路径）
	}
	for _, tc := range cases {
		if got := mouseIsPrimary(&MouseEvent{Button: tc.button}); got != tc.want {
			t.Errorf("button %d: primary=%v, want %v", tc.button, got, tc.want)
		}
	}
	if mouseIsPrimary(nil) {
		t.Fatal("nil event must not be primary")
	}
}
