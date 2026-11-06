// Package state persists deployment progress, the ledger of resources
// created by pk3s, and secrets (separately, mode 0600).
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// DefaultRoot is where pk3s keeps its data on the Proxmox host.
const DefaultRoot = "/var/lib/pk3s"

// Step statuses.
const (
	StatusPending = "pending"
	StatusRunning = "running"
	StatusDone    = "done"
	StatusFailed  = "failed"
)

// StepState records progress of one plan step.
type StepState struct {
	Status   string    `json:"status"`
	Started  time.Time `json:"started,omitempty"`
	Finished time.Time `json:"finished,omitempty"`
	Error    string    `json:"error,omitempty"`
}

// Resource kinds in the ledger.
const (
	KindContainer = "container"
	KindDataDir   = "data-dir"
	KindHostFile  = "host-file"
	KindTemplate  = "template"
)

// Resource is something pk3s created. Only ledger entries may ever be
// cleaned up, and only after confirmation.
type Resource struct {
	Kind    string    `json:"kind"`
	ID      string    `json:"id"`
	Node    string    `json:"node,omitempty"`
	Created time.Time `json:"created"`
}

// State is persisted as state.json.
type State struct {
	Cluster    string               `json:"cluster"`
	K3sVersion string               `json:"k3s_version"`
	Created    time.Time            `json:"created"`
	Updated    time.Time            `json:"updated"`
	Steps      map[string]StepState `json:"steps"`
	Resources  []Resource           `json:"resources"`
}

// Secrets is persisted as secrets.json (0600). Never log its contents.
type Secrets struct {
	ServerToken       string `json:"server_token"`
	AgentToken        string `json:"agent_token"`
	SecureServerToken string `json:"secure_server_token,omitempty"`
	SecureAgentToken  string `json:"secure_agent_token,omitempty"`
}

// Store manages the files of one cluster.
type Store struct {
	Root    string
	Cluster string
	lock    *os.File
}

// Dir returns the cluster directory.
func (s *Store) Dir() string { return filepath.Join(s.Root, "clusters", s.Cluster) }

// Path returns a file path inside the cluster directory.
func (s *Store) Path(name string) string { return filepath.Join(s.Dir(), name) }

// CacheDir returns the shared artifact cache.
func (s *Store) CacheDir() string { return filepath.Join(s.Root, "cache") }

// Exists reports whether state for the cluster exists.
func (s *Store) Exists() bool {
	_, err := os.Stat(s.Path("state.json"))
	return err == nil
}

// Init creates the directory tree with restrictive permissions.
func (s *Store) Init() error {
	for _, d := range []string{s.Root, filepath.Join(s.Root, "clusters"), s.Dir(), s.CacheDir(), s.Path("tmp")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
		if err := os.Chmod(d, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// Lock takes an exclusive, non-blocking lock so two pk3s processes cannot
// work on the same cluster.
func (s *Store) Lock() error {
	f, err := os.OpenFile(s.Path(".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	if err := lockFile(f); err != nil {
		f.Close()
		return fmt.Errorf("cluster %q is locked by another pk3s process", s.Cluster)
	}
	s.lock = f
	return nil
}

// Unlock releases the lock.
func (s *Store) Unlock() {
	if s.lock != nil {
		unlockFile(s.lock)
		s.lock.Close()
		s.lock = nil
	}
}

// Load reads state.json.
func (s *Store) Load() (*State, error) {
	var st State
	if err := readJSON(s.Path("state.json"), &st); err != nil {
		return nil, err
	}
	if st.Steps == nil {
		st.Steps = map[string]StepState{}
	}
	return &st, nil
}

// New returns a fresh state.
func New(cluster, version string) *State {
	now := time.Now().UTC()
	return &State{Cluster: cluster, K3sVersion: version, Created: now, Updated: now, Steps: map[string]StepState{}}
}

// Save writes state.json atomically.
func (s *Store) Save(st *State) error {
	st.Updated = time.Now().UTC()
	return writeJSON(s.Path("state.json"), st)
}

// LoadSecrets reads secrets.json.
func (s *Store) LoadSecrets() (*Secrets, error) {
	var sec Secrets
	if err := readJSON(s.Path("secrets.json"), &sec); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &Secrets{}, nil
		}
		return nil, err
	}
	return &sec, nil
}

// SaveSecrets writes secrets.json with mode 0600.
func (s *Store) SaveSecrets(sec *Secrets) error {
	return writeJSON(s.Path("secrets.json"), sec)
}

// WriteFile writes a file inside the cluster dir with mode 0600.
func (s *Store) WriteFile(name string, data []byte) (string, error) {
	p := s.Path(name)
	return p, atomicWrite(p, data)
}

// AddResource appends to the ledger if not present.
func (st *State) AddResource(r Resource) {
	for _, x := range st.Resources {
		if x.Kind == r.Kind && x.ID == r.ID {
			return
		}
	}
	if r.Created.IsZero() {
		r.Created = time.Now().UTC()
	}
	st.Resources = append(st.Resources, r)
}

// HasResource reports whether a resource is in the ledger.
func (st *State) HasResource(kind, id string) bool {
	for _, x := range st.Resources {
		if x.Kind == kind && x.ID == id {
			return true
		}
	}
	return false
}

// RemoveResource drops a ledger entry.
func (st *State) RemoveResource(kind, id string) {
	out := st.Resources[:0]
	for _, x := range st.Resources {
		if !(x.Kind == kind && x.ID == id) {
			out = append(out, x)
		}
	}
	st.Resources = out
}

// List returns cluster names with state under root.
func List(root string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, "clusters"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			if _, err := os.Stat(filepath.Join(root, "clusters", e.Name(), "state.json")); err == nil {
				names = append(names, e.Name())
			}
		}
	}
	sort.Strings(names)
	return names, nil
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(path, append(data, '\n'))
}

func atomicWrite(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
