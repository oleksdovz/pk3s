package runner

import (
	"context"
	"strings"
	"testing"
)

func TestRedactor(t *testing.T) {
	var r Redactor
	r.Add("short")
	r.Add("K10supersecret::server:abc")
	got := r.Redact("token K10supersecret::server:abc and short")
	if got != "token [REDACTED] and short" {
		t.Fatal(got)
	}
}

func TestExecReportsExitCode(t *testing.T) {
	_, err := Exec{}.Run(context.Background(), Cmd{Name: "sh", Args: []string{"-c", "echo boom >&2; exit 3"}})
	ee, ok := err.(*ExitError)
	if !ok || ee.ExitCode != 3 || ee.Stderr != "boom\n" {
		t.Fatalf("%#v", err)
	}
}

func TestOutputKeepsStdoutOnFailure(t *testing.T) {
	out, err := Output(context.Background(), Exec{}, Cmd{Name: "sh", Args: []string{"-c", "echo starting; exit 1"}})
	if err == nil || out != "starting" {
		t.Fatalf("%q %v", out, err)
	}
}

func TestExecLogsWithoutStdout(t *testing.T) {
	var lines []string
	e := Exec{Log: func(l string) { lines = append(lines, l) }}
	_, _ = e.Run(context.Background(), Cmd{Name: "sh", Args: []string{"-c", "cat; echo oops >&2; exit 2"}, Stdin: []byte("TOPSECRET")})
	if len(lines) != 1 {
		t.Fatalf("%v", lines)
	}
	l := lines[0]
	if strings.Contains(l, "TOPSECRET") || !strings.Contains(l, "-> exit 2") || !strings.Contains(l, "oops") || !strings.Contains(l, "<stdin 9 bytes>") {
		t.Fatal(l)
	}
}
