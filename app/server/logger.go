package server

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"
)

func Setup(debug bool) *slog.Logger {
	return SetupTo(debug, os.Stdout)
}

// SetupTo sama seperti Setup tetapi menulis ke w.
// Dipakai mode TUI agar log tidak merusak layar dan bisa tampil di window Logs.
func SetupTo(debug bool, w io.Writer) *slog.Logger {
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	h := slog.NewTextHandler(w, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey && len(groups) == 0 {
				if t, ok := a.Value.Any().(time.Time); ok {
					a.Value = slog.StringValue(t.Format(time.RFC3339))
				}
			}
			return a
		},
	})
	return slog.New(h)
}

func ReqID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err == nil {
		return hex.EncodeToString(b[:])
	}
	return fmt.Sprintf("%08x", uint32(time.Now().UnixNano()&0xffffffff))
}

func MaskKey(k string) string {
	if len(k) > 8 {
		return "****" + k[len(k)-4:]
	}
	return "****"
}
