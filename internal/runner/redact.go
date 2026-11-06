package runner

import (
	"strings"
	"sync"
)

// Redactor replaces registered secret values in text before it is logged
// or displayed. It is a defence in depth: secrets should not reach logs in
// the first place.
type Redactor struct {
	mu      sync.RWMutex
	secrets []string
}

// Add registers a secret. Short values are ignored to avoid mangling
// unrelated text.
func (r *Redactor) Add(secret string) {
	secret = strings.TrimSpace(secret)
	if len(secret) < 8 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.secrets {
		if s == secret {
			return
		}
	}
	r.secrets = append(r.secrets, secret)
}

// Redact returns s with all known secrets replaced.
func (r *Redactor) Redact(s string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, sec := range r.secrets {
		s = strings.ReplaceAll(s, sec, "[REDACTED]")
	}
	return s
}
