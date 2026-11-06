package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/oleksdovz/pk3s/internal/pve"
	"github.com/oleksdovz/pk3s/internal/spec"
	"github.com/oleksdovz/pk3s/internal/starter"
	"github.com/oleksdovz/pk3s/internal/state"
)

// InitResult is the output of InitSpec.
type InitResult struct {
	Content string
	// Existing is true when the cluster already exists and Content is its
	// deployed spec.
	Existing bool
}

// InitSpec scans the host and returns an annotated starter spec for a new
// cluster. If the cluster already exists (host state, or containers with
// its tag whose state can be restored from the ConfigMap), the deployed
// spec is returned instead so it can be edited and applied.
func (a *App) InitSpec(ctx context.Context, name, version string) (*InitResult, error) {
	inv, err := a.Inventory(ctx)
	if err != nil {
		return nil, err
	}
	store := &state.Store{Root: a.Root, Cluster: name}
	if !store.Exists() {
		if _, err := a.recoverState(ctx, name, inv); err != nil {
			return nil, err
		}
	}
	if store.Exists() {
		return a.exportDeployed(store)
	}
	return &InitResult{Content: a.starter(ctx, name, inv, version)}, nil
}

func (a *App) exportDeployed(store *state.Store) (*InitResult, error) {
	st, err := store.Load()
	if err != nil {
		return nil, err
	}
	s, err := spec.Load(store.Path("spec.yaml"))
	if err != nil {
		return nil, err
	}
	data, err := spec.Marshal(s)
	if err != nil {
		return nil, err
	}
	header := fmt.Sprintf("# Deployed spec of cluster %q (k3s %s, %d nodes), exported by `pk3s init`.\n"+
		"# Edit it (agents, servers, resources, k3s.version, ...) and run:\n"+
		"#   pk3s plan  -f <this file>   # shows only the changes\n"+
		"#   pk3s apply -f <this file>\n", s.Name, st.K3sVersion, len(st.Layout))
	if st.Pending != nil {
		header += fmt.Sprintf("# WARNING: update %s is still in progress; finish it with: pk3s resume %s\n", st.Pending.ID, s.Name)
	}
	return &InitResult{Content: header + string(data), Existing: true}, nil
}

func (a *App) starter(ctx context.Context, name string, inv *pve.Inventory, version string) string {
	s := spec.Default()
	s.Name = name
	ApplyInventoryDefaults(&s, inv)
	s.CTIDStart = starter.FreeCTIDStart(inv, spec.DefaultCTIDStart, s.TotalNodes()+starter.Headroom)
	opts := starter.Options{Now: time.Now(), Version: version}
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if v, err := a.Fetch.ResolveChannel(rctx, "stable"); err == nil {
		opts.StableVersion = v
	}
	return starter.Render(s, inv, opts)
}

// ExportSpec writes the deployed spec of a cluster to <name>.yaml in the
// current directory (a timestamped name if that file exists) and returns
// the path.
func (a *App) ExportSpec(ctx context.Context, name string) (string, error) {
	res, err := a.InitSpec(ctx, name, a.Version)
	if err != nil {
		return "", err
	}
	if !res.Existing {
		return "", fmt.Errorf("cluster %q is not deployed", name)
	}
	path := name + ".yaml"
	if _, err := os.Stat(path); err == nil {
		path = fmt.Sprintf("%s-%s.yaml", name, time.Now().Format("20060102-150405"))
	}
	if err := os.WriteFile(path, []byte(res.Content), 0o600); err != nil {
		return "", err
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return path, nil
}
