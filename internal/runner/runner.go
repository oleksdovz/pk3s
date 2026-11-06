// Package runner abstracts execution of external commands (pct, pvesh, ...)
// so that every component above it can be tested with a fake.
package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Cmd describes one external command invocation.
type Cmd struct {
	Name  string
	Args  []string
	Stdin []byte
	// SecretOutput marks stdout as sensitive: it is never logged.
	SecretOutput bool
}

// String renders the command for logs. Arguments must never contain
// secrets; secrets are passed through files or stdin.
func (c Cmd) String() string {
	parts := append([]string{c.Name}, c.Args...)
	for i, p := range parts {
		if p == "" || strings.ContainsAny(p, " \t\"'") {
			parts[i] = fmt.Sprintf("%q", p)
		}
	}
	return strings.Join(parts, " ")
}

// Result is the outcome of a finished command.
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// Runner executes commands.
type Runner interface {
	Run(ctx context.Context, cmd Cmd) (Result, error)
}

// ExitError is returned when a command exits with a non-zero status.
type ExitError struct {
	Cmd      string
	ExitCode int
	Stderr   string
}

func (e *ExitError) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if len(msg) > 2000 {
		msg = msg[len(msg)-2000:]
	}
	if msg == "" {
		return fmt.Sprintf("%s: exit status %d", e.Cmd, e.ExitCode)
	}
	return fmt.Sprintf("%s: exit status %d: %s", e.Cmd, e.ExitCode, msg)
}

// IsExitError reports whether err is a non-zero exit of a command.
func IsExitError(err error) bool {
	var ee *ExitError
	return errors.As(err, &ee)
}

// Exec runs commands on the local machine.
type Exec struct {
	// Log, if set, receives a line per command and its outcome.
	Log func(line string)
}

// Run implements Runner.
func (e Exec) Run(ctx context.Context, cmd Cmd) (Result, error) {
	c := exec.CommandContext(ctx, cmd.Name, cmd.Args...)
	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr
	if cmd.Stdin != nil {
		c.Stdin = bytes.NewReader(cmd.Stdin)
	}
	if e.Log != nil {
		e.Log("$ " + cmd.String())
	}
	err := c.Run()
	res := Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if err != nil {
		var xe *exec.ExitError
		if errors.As(err, &xe) {
			res.ExitCode = xe.ExitCode()
			return res, &ExitError{Cmd: cmd.String(), ExitCode: res.ExitCode, Stderr: string(res.Stderr)}
		}
		return res, fmt.Errorf("%s: %w", cmd.String(), err)
	}
	return res, nil
}

// Output runs cmd and returns trimmed stdout. Stdout is returned even when
// the command fails, because some tools report state via exit codes.
func Output(ctx context.Context, r Runner, cmd Cmd) (string, error) {
	res, err := r.Run(ctx, cmd)
	return strings.TrimSpace(string(res.Stdout)), err
}
