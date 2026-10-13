package ui

// select.go: model seleksi keyboard ala Windows Explorer untuk semua tabel.
//
// - Panah polos (lepas shift): mono-select (collapse) ke baris baru.
// - Shift+panah: extend/menyusut dari anchor; arah berlawanan = unselect.
// - Ctrl+panah: lompat collapse ke ujung (seksi/list).
// - Ctrl+Shift+panah / shift+home/end: extend sampai ujung.
// - Ctrl+A: select semua dalam seksi. Space: collapse ke focus.
// - Semua gerak berhenti di ujung (tanpa wrap), dan shift tidak menembus
//   batas seksi (mis. Keys vs Models di detail provider).

// selCollapse memindahkan focus ke `to` dan me-reset seleksi (mono-select).
func selCollapse(f *frame, to, n int) {
	f.sel = nil
	f.cursor = clamp(to, 0, n-1)
	f.anchor = f.cursor
}

// selExtend menggerakkan focus ke `to` (dijepit ke [lo,hi]) dan menghitung
// ulang seleksi sebagai rentang [anchor,focus]. Anchor di-reset ke focus
// bila belum ada seleksi atau anchor di luar seksi focus.
func selExtend(f *frame, to, lo, hi int) {
	if len(f.sel) == 0 || f.anchor < lo || f.anchor > hi {
		f.anchor = f.cursor
	}
	f.cursor = clamp(to, lo, hi)
	f.sel = provRange(f.anchor, f.cursor)
}

// provRange membangun seleksi rentang [lo,hi] (inklusif) ala shift-select
// Explorer: mengecil saat focus bergerak kembali ke anchor (auto-unselect).
func provRange(a, b int) map[int]bool {
	lo, hi := a, b
	if lo > hi {
		lo, hi = hi, lo
	}
	out := make(map[int]bool, hi-lo+1)
	for i := lo; i <= hi; i++ {
		out[i] = true
	}
	return out
}

// selSectionAll menselect semua baris [lo,hi] (satu seksi), focus tetap.
func selSectionAll(f *frame, lo, hi int) {
	if hi < lo {
		return
	}
	if f.sel == nil {
		f.sel = map[int]bool{}
	}
	for p := range f.sel {
		if p < lo || p > hi {
			delete(f.sel, p)
		}
	}
	for i := lo; i <= hi; i++ {
		f.sel[i] = true
	}
	f.anchor = f.cursor
}
