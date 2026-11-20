package pty

const (
	// minAxis matches the minimum dimension Start and Resize already accept.
	minAxis = 2
	// maxAxis is the largest value a Size axis (uint16) can hold.
	maxAxis = 65535
)

// FitSize constrains requested terminal dimensions into a usable range.
//
// cols and rows are the raw dimensions of a UI/window resize event. They are
// ints because such values are commonly derived from pixel measurements or
// decoded from JSON, and can be zero, negative or absurdly large. maxCols and
// maxRows bound the result to what the caller can accommodate; a non-positive
// bound means "no caller limit" and is capped only by what Size can hold.
//
// The result is always safe to pass to Resize: each axis is at least minAxis
// and at most the effective bound, and never exceeds uint16. minAxis wins over
// a smaller positive maximum so a degenerate bound cannot produce a useless
// size. FitSize is pure and safe for concurrent use.
func FitSize(cols, rows, maxCols, maxRows int) Size {
	return Size{
		Cols: fitAxis(cols, maxCols),
		Rows: fitAxis(rows, maxRows),
	}
}

// fitAxis clamps one dimension into [minAxis, effective max].
func fitAxis(value, max int) uint16 {
	if max <= 0 || max > maxAxis {
		max = maxAxis
	}
	if max < minAxis {
		max = minAxis
	}
	switch {
	case value < minAxis:
		return minAxis
	case value > max:
		return uint16(max)
	default:
		return uint16(value)
	}
}
