package logx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type red struct{}

func (red) Redact(s string) string { return strings.ReplaceAll(s, "SECRET", "[REDACTED]") }

func TestLogRedactsAndIsPrivate(t *testing.T) {
	p := filepath.Join(t.TempDir(), "pk3s.log")
	l, err := Open(p, red{})
	if err != nil {
		t.Fatal(err)
	}
	l.Printf("token %s", "SECRET")
	l.Printf("line1\nline2")
	l.Close()
	data, _ := os.ReadFile(p)
	s := string(data)
	if strings.Contains(s, "SECRET") || !strings.Contains(s, "token [REDACTED]") || !strings.Contains(s, "line1\n    line2") {
		t.Fatal(s)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
	var nilLogger *Logger
	nilLogger.Printf("no panic")
}

func TestRotation(t *testing.T) {
	p := filepath.Join(t.TempDir(), "pk3s.log")
	os.WriteFile(p, make([]byte, MaxSize+1), 0o600)
	l, err := Open(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
	if fi, err := os.Stat(p + ".1"); err != nil || fi.Size() != MaxSize+1 {
		t.Fatal("old log not rotated")
	}
}
