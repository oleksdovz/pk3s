// Package logx writes the pk3s activity log: one timestamped line per
// event, redacted, appended to a 0600 file with simple size rotation.
package logx

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// MaxSize triggers rotation to <path>.1 when the log is opened.
const MaxSize = 10 << 20

// Redactor removes secrets from text.
type Redactor interface{ Redact(string) string }

// Logger appends lines to a file. A nil *Logger is a valid no-op logger.
type Logger struct {
	mu   sync.Mutex
	f    *os.File
	red  Redactor
	Path string
	now  func() time.Time
}

// Open opens (and rotates if needed) the log file with mode 0600.
func Open(path string, red Redactor) (*Logger, error) {
	if fi, err := os.Stat(path); err == nil && fi.Size() > MaxSize {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	_ = f.Chmod(0o600)
	return &Logger{f: f, red: red, Path: path, now: time.Now}, nil
}

// Printf writes one entry; multi-line messages are indented.
func (l *Logger) Printf(format string, args ...any) {
	if l == nil {
		return
	}
	msg := fmt.Sprintf(format, args...)
	if l.red != nil {
		msg = l.red.Redact(msg)
	}
	msg = strings.ReplaceAll(strings.TrimRight(msg, "\n"), "\n", "\n    ")
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.f, "%s %s\n", l.now().Format("2006-01-02T15:04:05.000Z07:00"), msg)
}

// Close closes the file.
func (l *Logger) Close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_ = l.f.Close()
}
