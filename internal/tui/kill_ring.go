package tui

// 输入行的 Emacs kill-ring：连续删除合并 + yank / yank-pop 循环。
//
// 与 vim 键位正交：无论是否开启 ARGENT_VIM_MODE，删除键都会把被删文本记入环，
// Ctrl+Y 取回最近一次，Alt+Y 在环内循环取回更早的片段。环有界，所有截断按
// rune 边界进行，CJK/emoji 不会被切坏。纯标准库，无 I/O、无全局状态。

const (
	// killRingCapacity 是环中最多保留的片段数。
	killRingCapacity = 64
	// killRingMaxRunes 是单个片段保留的最大 rune 数。
	killRingMaxRunes = 4096
)

// killDirection 决定连续删除时新片段与既有片段的拼接方向。
type killDirection int

const (
	// killBackward（退格）删光标前的字符，新字符拼在既有片段之前。
	killBackward killDirection = iota
	// killForward（Delete）删光标处的字符，新字符拼在既有片段之后。
	killForward
)

// killRing 是「最近优先」的字符串环：entries[0] 是最近一次 kill。
type killRing struct {
	entries    []string
	capacity   int
	lastDir    killDirection
	accumulate bool // 上一段与本次是否属于同一串连续删除
	cycle      int  // yank-pop 当前位置
}

// newKillRing 返回一个默认容量的空环。
func newKillRing() killRing { return killRing{capacity: killRingCapacity} }

// Kill 记录一段被删除的文本。accumulate 为 true 且方向一致时与最近片段合并；
// 否则作为新片段插到队首。片段按 rune 上限裁剪，超出容量的最旧片段被丢弃。
func (r *killRing) Kill(text string, dir killDirection, accumulate bool) {
	if r == nil || text == "" {
		return
	}
	text = clipKillRunes(text, killRingMaxRunes)
	if text == "" {
		return
	}
	cap := r.capacity
	if cap <= 0 {
		cap = killRingCapacity
	}
	if accumulate && r.accumulate && r.lastDir == dir && len(r.entries) > 0 {
		if dir == killBackward {
			r.entries[0] = clipKillRunes(text+r.entries[0], killRingMaxRunes)
		} else {
			r.entries[0] = clipKillRunes(r.entries[0]+text, killRingMaxRunes)
		}
	} else {
		r.entries = append([]string{text}, r.entries...)
		if len(r.entries) > cap {
			r.entries = r.entries[:cap]
		}
	}
	r.lastDir = dir
	r.accumulate = true
	r.cycle = 0
}

// BreakChain 结束当前连续删除串。光标移动、插入、yank 之后调用，使下一次
// Kill 成为新片段，而不是并入上一次删除。
func (r *killRing) BreakChain() {
	if r == nil {
		return
	}
	r.accumulate = false
	r.cycle = 0
}

// Peek 返回最近片段但不改变环。
func (r *killRing) Peek() string {
	if r == nil || len(r.entries) == 0 {
		return ""
	}
	return r.entries[0]
}

// Yank 返回最近片段并把 yank-pop 位置复位到队首。
func (r *killRing) Yank() string {
	if r == nil {
		return ""
	}
	r.cycle = 0
	return r.Peek()
}

// YankPop 在环内循环：每次调用返回下一个更旧的片段，到末尾后回到最近片段。
// 它同时结束连续删除串，使随后的删除单独成条。
func (r *killRing) YankPop() string {
	if r == nil || len(r.entries) == 0 {
		return ""
	}
	r.cycle++
	if r.cycle >= len(r.entries) {
		r.cycle = 0
	}
	r.accumulate = false
	return r.entries[r.cycle]
}

// Len 返回环内片段数。
func (r *killRing) Len() int {
	if r == nil {
		return 0
	}
	return len(r.entries)
}

// clipKillRunes 按 rune 边界把 s 截到至多 limit 个 rune，避免切断 CJK/emoji。
func clipKillRunes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit])
}
