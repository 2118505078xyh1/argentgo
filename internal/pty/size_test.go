package pty

import "testing"

func TestFitSize(t *testing.T) {
	tests := []struct {
		name             string
		cols, rows       int
		maxCols, maxRows int
		want             Size
	}{
		{
			name: "normal request within bounds",
			cols: 80, rows: 24, maxCols: 200, maxRows: 60,
			want: Size{Cols: 80, Rows: 24},
		},
		{
			name: "exact bounds are kept",
			cols: 200, rows: 60, maxCols: 200, maxRows: 60,
			want: Size{Cols: 200, Rows: 60},
		},
		{
			name: "zero dimensions rise to the minimum",
			cols: 0, rows: 0, maxCols: 200, maxRows: 60,
			want: Size{Cols: minAxis, Rows: minAxis},
		},
		{
			name: "negative dimensions rise to the minimum",
			cols: -5, rows: -12, maxCols: 200, maxRows: 60,
			want: Size{Cols: minAxis, Rows: minAxis},
		},
		{
			name: "one zero axis only affects that axis",
			cols: 0, rows: 24, maxCols: 200, maxRows: 60,
			want: Size{Cols: minAxis, Rows: 24},
		},
		{
			name: "oversized dimensions clamp to the maximum",
			cols: 500, rows: 200, maxCols: 200, maxRows: 60,
			want: Size{Cols: 200, Rows: 60},
		},
		{
			name: "one oversized axis only affects that axis",
			cols: 500, rows: 24, maxCols: 200, maxRows: 60,
			want: Size{Cols: 200, Rows: 24},
		},
		{
			name: "zero maximum means no caller limit",
			cols: 500, rows: 200, maxCols: 0, maxRows: 0,
			want: Size{Cols: 500, Rows: 200},
		},
		{
			name: "negative maximum means no caller limit",
			cols: 500, rows: 200, maxCols: -1, maxRows: -1,
			want: Size{Cols: 500, Rows: 200},
		},
		{
			name: "maximum below the minimum yields the minimum",
			cols: 100, rows: 100, maxCols: 1, maxRows: 1,
			want: Size{Cols: minAxis, Rows: minAxis},
		},
		{
			name: "maximum above uint16 is capped",
			cols: 100000, rows: 100000, maxCols: 100000, maxRows: 100000,
			want: Size{Cols: maxAxis, Rows: maxAxis},
		},
		{
			name: "unbounded request is capped at uint16",
			cols: 100000, rows: 100000, maxCols: 0, maxRows: 0,
			want: Size{Cols: maxAxis, Rows: maxAxis},
		},
		{
			name: "mixed invalid request and bound",
			cols: -3, rows: 99999, maxCols: 80, maxRows: -5,
			want: Size{Cols: minAxis, Rows: maxAxis},
		},
		{
			name: "minimum request with tiny maximum stays usable",
			cols: 1, rows: 1, maxCols: 1, maxRows: 1,
			want: Size{Cols: minAxis, Rows: minAxis},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FitSize(tt.cols, tt.rows, tt.maxCols, tt.maxRows); got != tt.want {
				t.Fatalf("FitSize(%d, %d, %d, %d) = %+v, want %+v",
					tt.cols, tt.rows, tt.maxCols, tt.maxRows, got, tt.want)
			}
		})
	}
}

// TestFitSizeResultIsAlwaysResizable 检查核心不变量：无论什么
// TestFitSizeResultIsAlwaysResizable 检查核心不变量：无论 resize 事件带来什么
// 要求且能放进 uint16。
func TestFitSizeResultIsAlwaysResizable(t *testing.T) {
	inputs := []struct{ cols, rows, maxCols, maxRows int }{
		{0, 0, 0, 0},
		{-1, -1, -1, -1},
		{1, 1, 1, 1},
		{1 << 30, 1 << 30, 1 << 30, 1 << 30},
		{80, 24, 3, 3},
		{-100, 100, 0, 2},
	}
	for _, in := range inputs {
		got := FitSize(in.cols, in.rows, in.maxCols, in.maxRows)
		if got.Cols < minAxis || got.Rows < minAxis {
			t.Errorf("FitSize(%+v) = %+v: axes must be >= %d", in, got, minAxis)
		}
	}
}
