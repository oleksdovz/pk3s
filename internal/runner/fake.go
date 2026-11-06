package runner

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// Fake is a scripted Runner for tests. Responses are matched by command
// prefix (name + args joined with spaces); the longest matching prefix wins.
type Fake struct {
	mu        sync.Mutex
	responses map[string][]FakeResponse
	Calls     []Cmd
	// Default is returned for unmatched commands when Strict is false.
	Strict bool
}

// FakeResponse is one scripted answer.
type FakeResponse struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Err      error
}

// NewFake creates an empty strict fake.
func NewFake() *Fake {
	return &Fake{responses: map[string][]FakeResponse{}, Strict: true}
}

// On registers a response for commands starting with prefix. If several
// responses are registered for the same prefix they are returned in order
// and the last one repeats.
func (f *Fake) On(prefix string, resp FakeResponse) *Fake {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.responses[prefix] = append(f.responses[prefix], resp)
	return f
}

// OK registers a successful response with the given stdout.
func (f *Fake) OK(prefix, stdout string) *Fake {
	return f.On(prefix, FakeResponse{Stdout: stdout})
}

// Fail registers a failing response.
func (f *Fake) Fail(prefix string, code int, stderr string) *Fake {
	return f.On(prefix, FakeResponse{ExitCode: code, Stderr: stderr})
}

// Run implements Runner.
func (f *Fake) Run(_ context.Context, cmd Cmd) (Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, cmd)
	line := strings.Join(append([]string{cmd.Name}, cmd.Args...), " ")
	best := ""
	found := false
	for p := range f.responses {
		if strings.HasPrefix(line, p) && (!found || len(p) > len(best)) {
			best, found = p, true
		}
	}
	if !found {
		if f.Strict {
			return Result{ExitCode: 127}, fmt.Errorf("fake runner: unexpected command: %s", line)
		}
		return Result{}, nil
	}
	queue := f.responses[best]
	resp := queue[0]
	if len(queue) > 1 {
		f.responses[best] = queue[1:]
	}
	res := Result{Stdout: []byte(resp.Stdout), Stderr: []byte(resp.Stderr), ExitCode: resp.ExitCode}
	if resp.Err != nil {
		return res, resp.Err
	}
	if resp.ExitCode != 0 {
		return res, &ExitError{Cmd: cmd.String(), ExitCode: resp.ExitCode, Stderr: resp.Stderr}
	}
	return res, nil
}

// CallLines returns all recorded calls as strings.
func (f *Fake) CallLines() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.Calls))
	for i, c := range f.Calls {
		out[i] = strings.Join(append([]string{c.Name}, c.Args...), " ")
	}
	return out
}

// Called reports whether any call starts with prefix.
func (f *Fake) Called(prefix string) bool {
	for _, l := range f.CallLines() {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}
