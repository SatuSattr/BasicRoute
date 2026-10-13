package ui

import (
	"bytes"
	"strings"
	"sync"
)

// LogRing adalah penampung baris log berkapasitas tetap (ring).
// Dipakai sebagai io.Writer untuk slog agar log tidak merusak layar TUI
// dan bisa ditampilkan di window Logs. Aman untuk konkurensi.
type LogRing struct {
	mu      sync.Mutex
	lines   []string
	cap     int
	partial []byte
	ver     uint64 // naik setiap ada baris baru
}

// NewLogRing membuat ring dengan kapasitas baris n (min 50).
func NewLogRing(n int) *LogRing {
	if n < 50 {
		n = 50
	}
	return &LogRing{cap: n}
}

// Write memenuhi io.Writer. Data dipecah per baris ("\n").
func (r *LogRing) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.partial = append(r.partial, p...)
	for {
		i := bytes.IndexByte(r.partial, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimRight(string(r.partial[:i]), "\r")
		r.lines = append(r.lines, line)
		r.ver++
		rest := make([]byte, len(r.partial)-(i+1))
		copy(rest, r.partial[i+1:])
		r.partial = rest
	}
	if len(r.lines) > r.cap {
		kept := make([]string, r.cap)
		copy(kept, r.lines[len(r.lines)-r.cap:])
		r.lines = kept
	}
	return len(p), nil
}

// Snapshot mengembalikan salinan baris + nomor versi.
func (r *LogRing) Snapshot() (lines []string, ver uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.lines))
	copy(out, r.lines)
	return out, r.ver
}
